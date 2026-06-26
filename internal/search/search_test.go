package search

import (
	"context"
	"errors"
	"testing"

	meilisearch "github.com/meilisearch/meilisearch-go"
)

var errMeiliDown = errors.New("meilisearch down")

type fakeHealthySvc struct{ meilisearch.ServiceManager }

func (fakeHealthySvc) HealthWithContext(_ context.Context) (*meilisearch.Health, error) {
	return &meilisearch.Health{Status: "available"}, nil
}

type fakeUnhealthySvc struct{ meilisearch.ServiceManager }

func (fakeUnhealthySvc) HealthWithContext(_ context.Context) (*meilisearch.Health, error) {
	return nil, errMeiliDown
}

type fakeCanceledSvc struct{ meilisearch.ServiceManager }

func (fakeCanceledSvc) HealthWithContext(_ context.Context) (*meilisearch.Health, error) {
	return nil, context.Canceled
}

func TestPing_NilClient(t *testing.T) {
	var c *Client
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("nil client Ping: expected nil, got %v", err)
	}
}

func TestPing_Healthy(t *testing.T) {
	c := &Client{svc: fakeHealthySvc{}}
	if err := c.Ping(context.Background()); err != nil {
		t.Errorf("healthy Ping: expected nil, got %v", err)
	}
}

func TestPing_Unhealthy(t *testing.T) {
	c := &Client{svc: fakeUnhealthySvc{}}
	err := c.Ping(context.Background())
	if err == nil {
		t.Error("unhealthy Ping: expected error, got nil")
	} else if !errors.Is(err, errMeiliDown) {
		t.Errorf("unhealthy Ping: expected wrapped errMeiliDown, got %v", err)
	}
}

func TestPing_CanceledContext(t *testing.T) {
	c := &Client{svc: fakeCanceledSvc{}}
	err := c.Ping(context.Background())
	if err == nil {
		t.Error("canceled Ping: expected error, got nil")
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Ping: expected wrapped context.Canceled, got %v", err)
	}
}

func TestDeleteUserDocumentsAsync_NilSafe(t *testing.T) {
	var c *Client
	c.DeleteUserDocumentsAsync("any-user-id")
}
