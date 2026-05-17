package mailer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// sesEmailRequest is the SES API v2 SendEmail request body.
type sesEmailRequest struct {
	FromEmailAddress string         `json:"FromEmailAddress"`
	Destination      sesDestination `json:"Destination"`
	Content          sesContent     `json:"Content"`
}

type sesDestination struct {
	ToAddresses []string `json:"ToAddresses"`
}

type sesContent struct {
	Simple sesSimple `json:"Simple"`
}

type sesSimple struct {
	Subject sesCharset `json:"Subject"`
	Body    sesBody    `json:"Body"`
}

type sesCharset struct {
	Data    string `json:"Data"`
	Charset string `json:"Charset"`
}

type sesBody struct {
	Html sesCharset `json:"Html"`
}

func (m *Mailer) sendSES(ctx context.Context, to, subject, htmlBody string) error {
	payload, err := json.Marshal(sesEmailRequest{
		FromEmailAddress: m.sesFrom,
		Destination:      sesDestination{ToAddresses: []string{to}},
		Content: sesContent{
			Simple: sesSimple{
				Subject: sesCharset{Data: subject, Charset: "UTF-8"},
				Body:    sesBody{Html: sesCharset{Data: htmlBody, Charset: "UTF-8"}},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("ses marshal: %w", err)
	}

	url := "https://email." + m.sesRegion + ".amazonaws.com/v2/email/outbound-emails"
	xAmzDate, authorization := signSES(m.sesAccessKeyID, m.sesSecretKey, m.sesRegion, payload, time.Now().UTC())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("ses request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Amz-Date", xAmzDate)
	req.Header.Set("Authorization", authorization)

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("ses send: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ses: unexpected status %d: %s", resp.StatusCode, body)
	}
	return nil
}

// signSES produces the X-Amz-Date header value and the AWS4-HMAC-SHA256
// Authorization header for a SES API v2 SendEmail call.
// body must be the exact JSON bytes that will be sent as the request body.
func signSES(accessKeyID, secretKey, region string, body []byte, now time.Time) (xAmzDate, authorization string) {
	datetime := now.UTC().Format("20060102T150405Z")
	date := now.UTC().Format("20060102")
	host := "email." + region + ".amazonaws.com"

	bodyHash := hexSHA256(body)
	canonicalHeaders := "content-type:application/json\n" +
		"host:" + host + "\n" +
		"x-amz-date:" + datetime + "\n"
	signedHeaders := "content-type;host;x-amz-date"

	canonicalRequest := strings.Join([]string{
		"POST",
		"/v2/email/outbound-emails",
		"",
		canonicalHeaders,
		signedHeaders,
		bodyHash,
	}, "\n")

	credentialScope := date + "/" + region + "/ses/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		datetime,
		credentialScope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+secretKey), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, "ses")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	xAmzDate = datetime
	authorization = fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKeyID, credentialScope, signedHeaders, signature,
	)
	return
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
