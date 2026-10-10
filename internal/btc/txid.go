package btc

// IsTxid reports whether s is a syntactically valid 64-char hex txid.
func IsTxid(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		isHex := c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
		if !isHex {
			return false
		}
	}
	return true
}

// ShortTxid renders s for display: at most 10 chars plus an ellipsis.
func ShortTxid(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:10] + "…"
}
