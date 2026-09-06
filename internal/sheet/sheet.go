// Package sheet is a minimal Google Sheets reader authenticated with a
// service account (scope spreadsheets.readonly). It ports the Google Sheets
// usage of the original Java worker (MigrationService.syncStockPricelistFromSheet).
package sheet

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// Client reads cell ranges from Google Spreadsheets.
type Client struct {
	srv *sheets.Service
}

// NewClient reads the service-account JSON file at serviceAccountFile and
// returns a Sheets client. The file must belong to a service account that has
// been granted access to the target spreadsheet(s).
func NewClient(ctx context.Context, serviceAccountFile string) (*Client, error) {
	data, err := os.ReadFile(serviceAccountFile)
	if err != nil {
		return nil, fmt.Errorf("read service account %q: %w", serviceAccountFile, err)
	}
	creds, err := google.CredentialsFromJSON(ctx, data, sheets.SpreadsheetsReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("parse service account credentials: %w", err)
	}
	srv, err := sheets.NewService(ctx, option.WithCredentials(creds))
	if err != nil {
		return nil, fmt.Errorf("create sheets service: %w", err)
	}
	return &Client{srv: srv}, nil
}

// ReadRange fetches the values of the given A1 range (e.g.
// "'PRICELIST&MODAL'!A:D") of spreadsheetID. Cells are returned with the
// default FORMATTED_VALUE rendering, so values arrive as their display text.
// An empty sheet yields a nil slice (no error).
func (c *Client) ReadRange(ctx context.Context, spreadsheetID, rng string) ([][]any, error) {
	resp, err := c.srv.Spreadsheets.Values.Get(spreadsheetID, rng).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("get %s from spreadsheet %s: %w", rng, spreadsheetID, err)
	}
	return resp.Values, nil
}
