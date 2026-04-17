package auth

import (
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

var ErrNoBearer = errors.New("missing or invalid Authorization bearer")

type Context struct {
	UserID  string
	Email   string
	IsAdmin bool
}

func parseHS256(jwtSecret, rawToken string) (*Context, error) {
	jwtSecret = strings.TrimSpace(jwtSecret)
	if jwtSecret == "" {
		return nil, fmt.Errorf("empty jwt secret")
	}

	claims := jwt.MapClaims{}
	token, err := jwt.ParseWithClaims(rawToken, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method %v", t.Header["alg"])
		}
		return []byte(jwtSecret), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !token.Valid {
		return nil, fmt.Errorf("jwt: %w", err)
	}

	sub, _ := claims["sub"].(string)
	if sub == "" {
		return nil, fmt.Errorf("missing sub")
	}
	email, _ := claims["email"].(string)

	isAdmin := false
	if am, ok := claims["app_metadata"].(map[string]interface{}); ok {
		isAdmin = truthy(am["is_admin"])
	}

	return &Context{UserID: sub, Email: email, IsAdmin: isAdmin}, nil
}

func truthy(v interface{}) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return false
	}
}
