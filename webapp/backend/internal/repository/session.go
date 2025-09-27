package repository

import (
	"context"
	"errors"
	lru "github.com/hashicorp/golang-lru/v2"
	"time"

	"github.com/google/uuid"
)

const sessionCacheSize = 512

type sessionCacheEntry struct {
	userID    int
	expiresAt time.Time
}

type SessionRepository struct {
	db    DBTX
	cache *lru.Cache[string, sessionCacheEntry] // sessionID -> {userID, expiresAt}
}

func NewSessionRepository(db DBTX) *SessionRepository {
	cache, err := lru.New[string, sessionCacheEntry](sessionCacheSize)
	if err != nil {
		panic(err)
	}
	return &SessionRepository{
		db:    db,
		cache: cache,
	}
}

// セッションを作成し、セッションIDと有効期限を返す
func (r *SessionRepository) Create(ctx context.Context, userBusinessID int, duration time.Duration) (string, time.Time, error) {
	sessionUUID, err := uuid.NewRandom()
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := time.Now().Add(duration)
	sessionIDStr := sessionUUID.String()

	query := "INSERT INTO user_sessions (session_uuid, user_id, expires_at) VALUES (?, ?, ?)"
	_, err = r.db.ExecContext(ctx, query, sessionIDStr, userBusinessID, expiresAt)
	if err != nil {
		return "", time.Time{}, err
	}

	// キャッシュへ保存
	r.cache.Add(sessionIDStr, sessionCacheEntry{userID: userBusinessID, expiresAt: expiresAt})

	return sessionIDStr, expiresAt, nil
}

// セッションIDからユーザーIDを取得
func (r *SessionRepository) FindUserBySessionID(ctx context.Context, sessionID string) (int, error) {
	now := time.Now()

	// 先にキャッシュを確認 (あるはず)
	if v, ok := r.cache.Get(sessionID); ok {
		if now.Before(v.expiresAt) {
			return v.userID, nil
		}
		r.cache.Remove(sessionID)
		return 0, errors.New("session expired")
	}

	var userID int
	query := `
		SELECT 
			s.user_id
		FROM user_sessions s
		WHERE s.session_uuid = ? AND s.expires_at > ?`
	if err := r.db.GetContext(ctx, &userID, query, sessionID, now); err != nil {
		return 0, err
	}
	return userID, nil
}
