// Package store is the only place in the codebase that builds SQL.
//
// Tenancy rule (security-critical): every function that touches tenant-owned
// data takes companyID as its first argument after ctx, and every query
// filters on company_id. Callers never get a way to opt out.
package store

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Role string

const (
	RoleCustomer Role = "CUSTOMER"
	RoleEmployee Role = "EMPLOYEE"
)

type Channel string

const (
	ChannelCustomer Channel = "CUSTOMER"
	ChannelEmployee Channel = "EMPLOYEE"
)

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Pool exposes the underlying pool for transactional tool executors
// (pending-write commits in stage 4 run write + audit_log in one tx).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx, so audit inserts
// can join the transaction of the write they record.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}
