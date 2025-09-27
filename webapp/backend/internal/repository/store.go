package repository

import (
	"context"

	"github.com/jmoiron/sqlx"
)

type Store struct {
	db             DBTX
	orderRepoState *orderRepoState
	UserRepo       *UserRepository
	SessionRepo    *SessionRepository
	ProductRepo    *ProductRepository
	OrderRepo      *OrderRepository
}

func newStore(db DBTX, orderState *orderRepoState) *Store {
	if orderState == nil {
		orderState = &orderRepoState{}
	}
	store := &Store{
		db:             db,
		orderRepoState: orderState,
	}
	store.UserRepo = NewUserRepository(db)
	store.SessionRepo = NewSessionRepository(db)
	store.ProductRepo = NewProductRepository(db)
	store.OrderRepo = newOrderRepository(db, store.orderRepoState)
	return store
}

func NewStore(db DBTX) *Store {
	return newStore(db, &orderRepoState{})
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

	txStore := newStore(tx, s.orderRepoState)
	if err := fn(txStore); err != nil {
		return err
	}

	return tx.Commit()
}
