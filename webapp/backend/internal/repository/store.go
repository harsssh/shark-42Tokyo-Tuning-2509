package repository

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type Store struct {
	db DBTX

	sessionRepoState *sessionRepoState
	orderRepoState   *orderRepoState

	UserRepo    *UserRepository
	SessionRepo *SessionRepository
	ProductRepo *ProductRepository
	OrderRepo   *OrderRepository
}

// state を使う回すためのコンストラクタ
func newStore(db DBTX, sessionState *sessionRepoState, orderState *orderRepoState) *Store {
	store := &Store{
		db:               db,
		sessionRepoState: sessionState,
		orderRepoState:   orderState,
		UserRepo:         NewUserRepository(db),
		SessionRepo:      newSessionRepository(db, sessionState),
		ProductRepo:      NewProductRepository(db),
		OrderRepo:        newOrderRepository(db, orderState),
	}
	return store
}

func NewStore(db DBTX) *Store {
	return newStore(db, &sessionRepoState{}, &orderRepoState{})
}

func (s *Store) ExecTx(ctx context.Context, fn func(txStore *Store) error) error {
	db, ok := s.db.(*sqlx.DB)
	if !ok {
		return fn(s)
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	txStore := newStore(tx, s.sessionRepoState, s.orderRepoState)
	if err := fn(txStore); err != nil {
		return err
	}

	return tx.Commit()
}
