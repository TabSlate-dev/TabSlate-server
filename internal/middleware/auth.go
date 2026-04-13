package middleware

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/tabslate/server/internal/auth"
)

const UserIDKey = "userID"

// Auth extracts and validates the Bearer JWT from the Authorization header.
// On success it sets `userID` in the Gin context.
func Auth(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" || !strings.HasPrefix(header, "Bearer ") {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing authorization header"})
			return
		}

		tokenStr := strings.TrimPrefix(header, "Bearer ")
		claims, err := auth.ParseAccessToken(tokenStr, jwtSecret)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid or expired token"})
			return
		}

		c.Set(UserIDKey, claims.UserID)
		c.Next()
	}
}

// UserID retrieves the authenticated user ID from the Gin context.
// Panics if called outside of the Auth middleware.
func UserID(c *gin.Context) string {
	return c.MustGet(UserIDKey).(string)
}
