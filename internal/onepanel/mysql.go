package onepanel

import (
	"context"
	"net/http"
	"time"
)

// MySQL and MariaDB databases that 1Panel manages inside a database app.

// MySQLDB is one database.
type MySQLDB struct {
	ID          uint      `json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	Name        string    `json:"name"`
	From        string    `json:"from"`      // local: the app on this server
	MysqlName   string    `json:"mysqlName"` // the database app
	Format      string    `json:"format"`    // character set, e.g. utf8mb4
	Username    string    `json:"username"`
	Permission  string    `json:"permission"` // who may connect: %, localhost or an IP
	Description string    `json:"description"`
}

// MySQLDatabases lists the databases of a database app.
func (c *Client) MySQLDatabases(ctx context.Context, app string) ([]MySQLDB, error) {
	var page struct {
		Items []MySQLDB `json:"items"`
	}
	err := c.do(ctx, http.MethodPost, "/databases/search",
		map[string]any{"page": 1, "pageSize": 200, "database": app, "orderBy": "createdAt", "order": "null"}, &page)
	if page.Items == nil {
		page.Items = []MySQLDB{}
	}
	return page.Items, err
}

// CreateMySQLDB creates a database and a user with all rights on it.
func (c *Client) CreateMySQLDB(ctx context.Context, app, name, user, password, permission, description string) error {
	return c.do(ctx, http.MethodPost, "/databases", map[string]any{
		"name": name, "from": "local", "database": app, "format": "utf8mb4", "username": user, "password": password,
		"permission": permission, "description": description,
	}, nil)
}

// DeleteMySQLDB drops a database and its user; its backups are kept.
func (c *Client) DeleteMySQLDB(ctx context.Context, appType, app string, id uint) error {
	return c.do(ctx, http.MethodPost, "/databases/del", map[string]any{
		"id": id, "type": appType, "database": app, "forceDelete": false, "deleteBackup": false,
	}, nil)
}
