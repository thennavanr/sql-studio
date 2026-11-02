package engine

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
)

// maxCellBytes caps very large binary / text cells sent to the UI.
const maxCellBytes = 2 << 20 // 2 MiB

// FormatValue renders a scanned value as SSMS-like text. NULL becomes nil so
// the UI can distinguish it from the string "NULL". Everything else is a string
// to avoid precision loss for bigint/decimal in JavaScript.
func FormatValue(v any, dbType string) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		switch dbType {
		case "DECIMAL", "NUMERIC", "MONEY", "SMALLMONEY":
			return string(x)
		case "UNIQUEIDENTIFIER":
			var u mssql.UniqueIdentifier
			if err := u.Scan(x); err == nil {
				return u.String()
			}
		}
		if len(x) > maxCellBytes {
			return "0x" + strings.ToUpper(hex.EncodeToString(x[:maxCellBytes])) + "…"
		}
		return "0x" + strings.ToUpper(hex.EncodeToString(x))
	case string:
		if len(x) > maxCellBytes {
			return x[:maxCellBytes] + "…"
		}
		return x
	case bool:
		if x {
			return "1"
		}
		return "0"
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if dbType == "REAL" {
			return strconv.FormatFloat(x, 'g', -1, 32)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case time.Time:
		switch dbType {
		case "DATE":
			return x.Format("2006-01-02")
		case "TIME":
			return x.Format("15:04:05.0000000")
		case "SMALLDATETIME":
			return x.Format("2006-01-02 15:04:00")
		case "DATETIME":
			return x.Format("2006-01-02 15:04:05.000")
		case "DATETIMEOFFSET":
			return x.Format("2006-01-02 15:04:05.0000000 -07:00")
		default: // DATETIME2
			return x.Format("2006-01-02 15:04:05.0000000")
		}
	default:
		return fmt.Sprint(x)
	}
}

// IsNumericType reports whether values of this type should be right-aligned.
func IsNumericType(dbType string) bool {
	switch dbType {
	case "INT", "BIGINT", "SMALLINT", "TINYINT", "DECIMAL", "NUMERIC", "MONEY",
		"SMALLMONEY", "FLOAT", "REAL", "BIT":
		return true
	}
	return false
}
