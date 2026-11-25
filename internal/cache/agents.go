// SPDX-License-Identifier: MPL-2.0
package cache

import (
	"fmt"
	"time"

	"github.com/luckzk/zy-drive/internal/domain"
)

// SaveAgent upserts one Agent record. Only one Agent may be default:
// setting Default here clears the flag on every other row.
func (db *DB) SaveAgent(a *domain.Agent) error {
	tx, err := db.SQL.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if a.Default {
		if _, err := tx.Exec(`UPDATE agents SET is_default = 0`); err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	var lastHB *string
	if a.LastHeartbeat != nil {
		s := a.LastHeartbeat.UTC().Format(time.RFC3339)
		lastHB = &s
	}
	if _, err := tx.Exec(`INSERT INTO agents
		(id, name, address, token_ref, online, version, protocol_version,
		 jobs_running, last_heartbeat, is_default, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, address=excluded.address,
			token_ref=excluded.token_ref, online=excluded.online,
			version=excluded.version, protocol_version=excluded.protocol_version,
			jobs_running=excluded.jobs_running, last_heartbeat=excluded.last_heartbeat,
			is_default=excluded.is_default, updated_at=excluded.updated_at`,
		a.ID, a.Name, a.Address, a.TokenRef, boolToInt(a.Online), a.Version,
		a.ProtocolVersion, a.JobsRunning, lastHB, boolToInt(a.Default), now, now); err != nil {
		return fmt.Errorf("cache: save agent: %w", err)
	}
	return tx.Commit()
}

// ListAgents returns all Agents in creation order.
func (db *DB) ListAgents() ([]*domain.Agent, error) {
	rows, err := db.SQL.Query(`SELECT id, name, address, token_ref, online,
		version, protocol_version, jobs_running, last_heartbeat, is_default,
		created_at, updated_at FROM agents ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("cache: list agents: %w", err)
	}
	defer rows.Close()
	var out []*domain.Agent
	for rows.Next() {
		var a domain.Agent
		var online, isDefault int
		var lastHB, created, updated *string
		if err := rows.Scan(&a.ID, &a.Name, &a.Address, &a.TokenRef, &online,
			&a.Version, &a.ProtocolVersion, &a.JobsRunning, &lastHB, &isDefault,
			&created, &updated); err != nil {
			return nil, err
		}
		a.Online = online != 0
		a.Default = isDefault != 0
		if lastHB != nil {
			t, _ := time.Parse(time.RFC3339, *lastHB)
			a.LastHeartbeat = &t
		}
		if created != nil {
			a.CreatedAt, _ = time.Parse(time.RFC3339, *created)
		}
		if updated != nil {
			a.UpdatedAt, _ = time.Parse(time.RFC3339, *updated)
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

// DeleteAgent removes one Agent. Jobs naming it keep their AgentID;
// the UI must re-point them (to the default Agent) before running.
func (db *DB) DeleteAgent(id string) error {
	_, err := db.SQL.Exec(`DELETE FROM agents WHERE id = ?`, id)
	return err
}

// DefaultAgent returns the Agent flagged default, or nil.
func (db *DB) DefaultAgent() (*domain.Agent, error) {
	agents, err := db.ListAgents()
	if err != nil {
		return nil, err
	}
	for _, a := range agents {
		if a.Default {
			return a, nil
		}
	}
	return nil, nil
}
