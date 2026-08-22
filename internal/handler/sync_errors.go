package handler

import (
	"errors"
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgconn"
)

const maxLoggedSyncEntityIDs = 10

func syncPostgresError(err error) *pgconn.PgError {
	var postgresError *pgconn.PgError
	if errors.As(err, &postgresError) {
		return postgresError
	}
	return nil
}

func syncDatabaseHTTPStatus(err error) int {
	postgresError := syncPostgresError(err)
	if postgresError != nil && (postgresError.Code == "40001" || postgresError.Code == "40P01") {
		return http.StatusServiceUnavailable
	}
	return http.StatusInternalServerError
}

func boundedSyncEntityIDs(ids []string) []string {
	if len(ids) <= maxLoggedSyncEntityIDs {
		return ids
	}
	return ids[:maxLoggedSyncEntityIDs]
}

func respondSyncDatabaseError(
	ginContext *gin.Context,
	operation string,
	userID string,
	entityIDs []string,
	err error,
) {
	status := syncDatabaseHTTPStatus(err)
	postgresError := syncPostgresError(err)
	if postgresError == nil {
		log.Printf("sync push operation=%q user_id=%q entity_ids=%q error=%v",
			operation, userID, boundedSyncEntityIDs(entityIDs), err)
	} else {
		log.Printf(
			"sync push operation=%q user_id=%q entity_ids=%q sqlstate=%q table=%q constraint=%q error=%v",
			operation, userID, boundedSyncEntityIDs(entityIDs), postgresError.Code,
			postgresError.TableName, postgresError.ConstraintName, err,
		)
	}
	message := "sync failed"
	if status == http.StatusServiceUnavailable {
		message = "sync temporarily unavailable"
	}
	ginContext.JSON(status, gin.H{"error": message})
}
