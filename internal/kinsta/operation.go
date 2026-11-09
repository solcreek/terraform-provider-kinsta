// SPDX-License-Identifier: MPL-2.0

package kinsta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Accepted is the 202 body Kinsta answers asynchronous writes with.
// OperationID is optional in the spec (DNS writes may omit it).
type Accepted struct {
	OperationID string `json:"operation_id"`
	Message     string `json:"message"`
	Status      int    `json:"status"`
}

// Operation is the body of GET /operations/{operation_id}. The HTTP status
// mirrors Status: 202 while running, 200 when finished, 500 when failed.
type Operation struct {
	Status  int             `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// OperationError is an operation that finished unsuccessfully.
type OperationError struct {
	ID      string
	Status  int
	Message string
}

func (e *OperationError) Error() string {
	return fmt.Sprintf("kinsta operation %s failed (%d): %s", e.ID, e.Status, e.Message)
}

// GetOperation fetches one operation's current status.
func (c *Client) GetOperation(ctx context.Context, id string) (*Operation, error) {
	raw, err := c.send(ctx, http.MethodGet, "/operations/"+id, nil, nil)
	if err != nil {
		return nil, err
	}
	switch raw.status {
	case http.StatusOK, http.StatusAccepted, http.StatusInternalServerError:
		op := &Operation{}
		if err := json.Unmarshal(raw.body, op); err != nil {
			if raw.status == http.StatusInternalServerError {
				return nil, parseError(raw.status, raw.body)
			}
			return nil, fmt.Errorf("decode operation %s: %w", id, err)
		}
		if op.Status == 0 {
			op.Status = raw.status
		}
		return op, nil
	default:
		return nil, parseError(raw.status, raw.body)
	}
}

// operationNotFoundGrace is how long a freshly created operation may answer
// 404 before it is treated as missing.
const operationNotFoundGrace = 60 * time.Second

// WaitOperation polls an operation until it finishes, the context ends, or
// it fails. A finished operation is returned with a nil error; a failed one
// returns an *OperationError carrying Kinsta's reason.
func (c *Client) WaitOperation(ctx context.Context, id string) (*Operation, error) {
	if id == "" {
		return nil, nil
	}
	started := time.Now()
	for {
		op, err := c.GetOperation(ctx, id)
		if err != nil && ctx.Err() != nil {
			return nil, fmt.Errorf("operation %s still running when the deadline passed (check MyKinsta): %w", id, ctx.Err())
		}
		switch {
		case err != nil && IsNotFound(err) && time.Since(started) < operationNotFoundGrace:
			// A fresh operation can 404 briefly before it is registered.
		case err != nil:
			return nil, fmt.Errorf("poll operation %s: %w", id, err)
		case op.Status == http.StatusAccepted:
			// Still running.
		case op.Status >= 200 && op.Status < 300:
			return op, nil
		default:
			msg := op.Message
			if detail := dataMessage(op.Data); detail != "" {
				msg = detail
			}
			return op, &OperationError{ID: id, Status: op.Status, Message: msg}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("operation %s still running when the deadline passed (check MyKinsta): %w", id, ctx.Err())
		case <-time.After(c.pollInterval):
		}
	}
}

// settle waits for the operation an Accepted response names, if any.
func (c *Client) settle(ctx context.Context, a *Accepted) (*Operation, error) {
	if a == nil || a.OperationID == "" {
		return nil, nil
	}
	return c.WaitOperation(ctx, a.OperationID)
}

// DataString reads a string field from an operation's data object, such as
// the new site id (`idSite`) a site creation reports.
func (o *Operation) DataString(key string) string {
	if o == nil || len(o.Data) == 0 {
		return ""
	}
	var m map[string]any
	if json.Unmarshal(o.Data, &m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}
