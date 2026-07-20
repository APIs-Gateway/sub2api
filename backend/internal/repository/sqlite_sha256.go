package repository

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/hex"
	"fmt"

	sqlite "modernc.org/sqlite"
)

// SQLite does not provide SHA-256 as a built-in SQL function.  The SQLite
// migration uses this deterministic UDF so its triggers produce the same
// cache-key format as PostgreSQL and MySQL without persisting plaintext keys.
func init() {
	if err := sqlite.RegisterFunction("sha256", &sqlite.FunctionImpl{
		NArgs:         1,
		Deterministic: true,
		Scalar: func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			if len(args) != 1 || args[0] == nil {
				return nil, nil
			}
			var raw []byte
			switch value := args[0].(type) {
			case string:
				raw = []byte(value)
			case []byte:
				raw = value
			default:
				raw = []byte(fmt.Sprint(value))
			}
			digest := sha256.Sum256(raw)
			return hex.EncodeToString(digest[:]), nil
		},
	}); err != nil {
		panic("register sqlite sha256 UDF: " + err.Error())
	}
}
