package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"time"

	"backend/internal/repository"
	"backend/internal/service/utils"

	"go.opentelemetry.io/otel"
	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUserNotFound    = errors.New("user not found")
	ErrInvalidPassword = errors.New("invalid password")
	ErrInternalServer  = errors.New("internal server error")
)

type AuthService struct {
	store         *repository.Store
	passwordCache *sync.Map
}

func NewAuthService(store *repository.Store) *AuthService {
	return &AuthService{store: store, passwordCache: &sync.Map{}}
}

func makePasswordCacheKey(passwordHash, password string) string {
	digest := sha256.Sum256([]byte(password))
	return passwordHash + ":" + hex.EncodeToString(digest[:])
}

func (s *AuthService) Login(ctx context.Context, userName, password string) (string, time.Time, error) {
	ctx, span := otel.Tracer("service.auth").Start(ctx, "AuthService.Login")
	defer span.End()

	var sessionID string
	var expiresAt time.Time
	err := utils.WithTimeout(ctx, func(ctx context.Context) error {
		user, err := s.store.UserRepo.FindByUserName(ctx, userName)
		if err != nil {
			log.Printf("[Login] ユーザー検索失敗(userName: %s): %v", userName, err)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrUserNotFound
			}
			return ErrInternalServer
		}

		cacheKey := makePasswordCacheKey(user.PasswordHash, password)
		if _, ok := s.passwordCache.Load(cacheKey); !ok {
			err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password))
			if err != nil {
				log.Printf("[Login] パスワード検証失敗: %v", err)
				span.RecordError(err)
				return ErrInvalidPassword
			}
			s.passwordCache.Store(cacheKey, struct{}{})
		}

		sessionDuration := 24 * time.Hour
		sessionID, expiresAt, err = s.store.SessionRepo.Create(ctx, user.UserID, sessionDuration)
		if err != nil {
			log.Printf("[Login] セッション生成失敗: %v", err)
			return ErrInternalServer
		}
		return nil
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return sessionID, expiresAt, nil
}
