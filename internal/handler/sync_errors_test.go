package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestSyncDatabaseHTTPStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "serialization failure", err: &pgconn.PgError{Code: "40001"}, want: http.StatusServiceUnavailable},
		{name: "deadlock", err: &pgconn.PgError{Code: "40P01"}, want: http.StatusServiceUnavailable},
		{name: "foreign key violation", err: &pgconn.PgError{Code: "23503"}, want: http.StatusInternalServerError},
		{name: "non postgres error", err: errors.New("database unavailable"), want: http.StatusInternalServerError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := syncDatabaseHTTPStatus(test.err); got != test.want {
				t.Fatalf("status = %d, want %d", got, test.want)
			}
		})
	}
}

func TestRespondSyncDatabaseErrorDoesNotExposePostgresDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	postgresError := &pgconn.PgError{
		Code:           "23503",
		TableName:      "collections",
		ConstraintName: "bookmarks_collection_id_fkey",
		Message:        "insert or update on table \"bookmarks\" violates foreign key constraint \"bookmarks_collection_id_fkey\"",
	}

	respondSyncDatabaseError(ginContext, "bookmark upsert", "user-1", []string{"bookmark-1"}, postgresError)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if got := recorder.Body.String(); got != "{\"error\":\"sync failed\"}" {
		t.Fatalf("body = %q, want sync failed body", got)
	}
	for _, forbidden := range []string{
		postgresError.Code,
		postgresError.TableName,
		postgresError.ConstraintName,
		postgresError.Message,
	} {
		if strings.Contains(recorder.Body.String(), forbidden) {
			t.Fatalf("body leaked postgres detail %q: %s", forbidden, recorder.Body.String())
		}
	}
}

func TestRespondSyncDatabaseErrorMarksSerializationFailuresAsTransient(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)

	respondSyncDatabaseError(ginContext, "transaction commit", "user-1", nil, &pgconn.PgError{Code: "40001"})

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Body.String(); got != "{\"error\":\"sync temporarily unavailable\"}" {
		t.Fatalf("body = %q, want transient sync error body", got)
	}
}

func TestBoundedSyncEntityIDsLimitsDiagnosticScope(t *testing.T) {
	ids := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}

	got := boundedSyncEntityIDs(ids)

	if len(got) != 10 {
		t.Fatalf("bounded IDs length = %d, want 10", len(got))
	}
	if got[0] != "1" || got[9] != "10" {
		t.Fatalf("bounded IDs = %#v, want first ten IDs", got)
	}
}
