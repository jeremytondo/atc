package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeremytondo/atc/internal/publicid"
)

// Action is one persisted server-wide launch recipe.
type Action struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
	Command     string
	Args        []string
	IsAgent     bool
}

// UpdateActionInput distinguishes omitted fields from fields set by PATCH.
// DescriptionSet allows Description=nil to clear the nullable description.
type UpdateActionInput struct {
	Name           *string
	DescriptionSet bool
	Description    *string
	Enabled        *bool
	Command        *string
	Args           *[]string
	IsAgent        *bool
}

// CreateAction validates and inserts a new Action with a generated public ID.
func (s *Store) CreateAction(ctx context.Context, action Action) (Action, error) {
	action.Name = strings.TrimSpace(action.Name)
	action.Command = strings.TrimSpace(action.Command)
	action.Args = normalizeArgs(action.Args)
	if err := validateAction(action); err != nil {
		return Action{}, err
	}
	id, err := publicid.New("act_")
	if err != nil {
		return Action{}, err
	}
	action.ID = id
	args, err := json.Marshal(action.Args)
	if err != nil {
		return Action{}, fmt.Errorf("%w: args: %v", ErrInvalidAction, err)
	}
	created, err := scanAction(s.db.QueryRowContext(ctx, `
		INSERT INTO actions (id, name, description, enabled, command, args, is_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING`+actionColumnsSQL,
		action.ID,
		action.Name,
		nullableString(action.Description),
		action.Enabled,
		action.Command,
		string(args),
		action.IsAgent,
	))
	if err != nil {
		return Action{}, fmt.Errorf("create action %s: %w", action.ID, err)
	}
	return created, nil
}

// ListActions returns all Actions ordered by display name and then ID.
func (s *Store) ListActions(ctx context.Context) ([]Action, error) {
	rows, err := s.db.QueryContext(ctx, selectActionSQL+` ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	defer rows.Close()

	actions := make([]Action, 0)
	for rows.Next() {
		action, err := scanAction(rows)
		if err != nil {
			return nil, fmt.Errorf("list actions: %w", err)
		}
		actions = append(actions, action)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list actions: %w", err)
	}
	return actions, nil
}

// GetAction loads an Action by its opaque ID.
func (s *Store) GetAction(ctx context.Context, id string) (Action, error) {
	action, err := scanAction(s.db.QueryRowContext(ctx, selectActionSQL+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, fmt.Errorf("%w: %s", ErrActionNotFound, id)
	}
	if err != nil {
		return Action{}, fmt.Errorf("get action %s: %w", id, err)
	}
	return action, nil
}

// UpdateAction applies a partial update and returns the complete Action.
func (s *Store) UpdateAction(ctx context.Context, id string, input UpdateActionInput) (Action, error) {
	current, err := s.GetAction(ctx, id)
	if err != nil {
		return Action{}, err
	}
	if input.Name != nil {
		current.Name = strings.TrimSpace(*input.Name)
	}
	if input.DescriptionSet {
		current.Description = ""
		if input.Description != nil {
			current.Description = *input.Description
		}
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if input.Command != nil {
		current.Command = strings.TrimSpace(*input.Command)
	}
	if input.Args != nil {
		current.Args = normalizeArgs(*input.Args)
	}
	if input.IsAgent != nil {
		current.IsAgent = *input.IsAgent
	}
	if err := validateAction(current); err != nil {
		return Action{}, err
	}
	args, err := json.Marshal(current.Args)
	if err != nil {
		return Action{}, fmt.Errorf("%w: args: %v", ErrInvalidAction, err)
	}
	updated, err := scanAction(s.db.QueryRowContext(ctx, `
		UPDATE actions
		SET name = ?, description = ?, enabled = ?, command = ?, args = ?, is_agent = ?
		WHERE id = ?
		RETURNING`+actionColumnsSQL,
		current.Name,
		nullableString(current.Description),
		current.Enabled,
		current.Command,
		string(args),
		current.IsAgent,
		id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, fmt.Errorf("%w: %s", ErrActionNotFound, id)
	}
	if err != nil {
		return Action{}, fmt.Errorf("update action %s: %w", id, err)
	}
	return updated, nil
}

// DeleteAction permanently deletes an Action without inspecting sessions.
func (s *Store) DeleteAction(ctx context.Context, id string) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM actions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete action %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete action %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("%w: %s", ErrActionNotFound, id)
	}
	return nil
}

func validateAction(action Action) error {
	if action.Name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidAction)
	}
	if action.Command == "" {
		return fmt.Errorf("%w: command is required", ErrInvalidAction)
	}
	return nil
}

func normalizeArgs(args []string) []string {
	if args == nil {
		return make([]string, 0)
	}
	out := make([]string, len(args))
	copy(out, args)
	return out
}

const actionColumnsSQL = `
		id,
		name,
		COALESCE(description, ''),
		enabled,
		command,
		args,
		is_agent`

const selectActionSQL = `
SELECT` + actionColumnsSQL + `
	FROM actions`

func scanAction(row scanner) (Action, error) {
	var action Action
	var args string
	if err := row.Scan(
		&action.ID,
		&action.Name,
		&action.Description,
		&action.Enabled,
		&action.Command,
		&args,
		&action.IsAgent,
	); err != nil {
		return Action{}, err
	}
	if err := json.Unmarshal([]byte(args), &action.Args); err != nil {
		return Action{}, fmt.Errorf("decode action args for %s: %w", action.ID, err)
	}
	action.Args = normalizeArgs(action.Args)
	return action, nil
}
