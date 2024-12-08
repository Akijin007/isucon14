package main

import (
	"context"
	"errors"
	"net/http"
	"sync"
)

var userTokenCache = sync.Map{}
var ownerTokenCache = sync.Map{}
var chairTokenCache = sync.Map{}

func getUserFromToken(token string) (User, bool) {
	if item, ok := userTokenCache.Load(token); ok {
		return item.(User), true
	}
	return User{}, false
}
func getOwnerFromToken(token string) (Owner, bool) {
	if item, ok := ownerTokenCache.Load(token); ok {
		return item.(Owner), true
	}
	return Owner{}, false
}
func getChairFromToken(token string) (Chair, bool) {
	if item, ok := chairTokenCache.Load(token); ok {
		return item.(Chair), true
	}
	return Chair{}, false
}

func appAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := r.Cookie("app_session")
		if errors.Is(err, http.ErrNoCookie) || c.Value == "" {
			writeError(w, http.StatusUnauthorized, errors.New("app_session cookie is required"))
			return
		}
		accessToken := c.Value

		user, exist := getUserFromToken(accessToken)
		if !exist {
			writeError(w, http.StatusUnauthorized, errors.New("invalid access token"))
			return
		}

		ctx = context.WithValue(ctx, "user", &user)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func ownerAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := r.Cookie("owner_session")
		if errors.Is(err, http.ErrNoCookie) || c.Value == "" {
			writeError(w, http.StatusUnauthorized, errors.New("owner_session cookie is required"))
			return
		}
		accessToken := c.Value
		owner, exist := getOwnerFromToken(accessToken)
		if !exist {
			writeError(w, http.StatusUnauthorized, errors.New("invalid access token"))
			return
		}

		ctx = context.WithValue(ctx, "owner", &owner)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func chairAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := r.Cookie("chair_session")
		if errors.Is(err, http.ErrNoCookie) || c.Value == "" {
			writeError(w, http.StatusUnauthorized, errors.New("chair_session cookie is required"))
			return
		}
		accessToken := c.Value
		chair, exist := getChairFromToken(accessToken)
		if !exist {
			writeError(w, http.StatusUnauthorized, errors.New("invalid access token"))
			return
		}

		ctx = context.WithValue(ctx, "chair", &chair)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
