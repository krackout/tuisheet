package locale

import (
	"os"
	"strings"
)

var cachedEnvKey, cachedTag string

func envKey() string {
	// Single allocation: LC_ALL is most common override
	return os.Getenv("LC_ALL") + "|" + os.Getenv("LC_NUMERIC") + "|" + os.Getenv("LANGUAGE") + "|" + os.Getenv("LANG")
}

func Tag() string {
	k := envKey()
	if k == cachedEnvKey && cachedTag != "" {
		return cachedTag
	}
	for _, key := range []string{"LC_ALL", "LC_NUMERIC", "LANG", "LANGUAGE"} {
		if v := os.Getenv(key); v != "" {
			if key == "LANGUAGE" {
				if idx := strings.Index(v, ":"); idx >= 0 {
					v = v[:idx]
				}
			}
			v = strings.TrimSpace(v)
			if v == "" || v == "C" || v == "POSIX" {
				continue
			}
			if idx := strings.Index(v, "."); idx >= 0 {
				v = v[:idx]
			}
			if idx := strings.Index(v, "@"); idx >= 0 {
				v = v[:idx]
			}
			v = strings.ReplaceAll(v, "_", "-")
			if v != "" {
				cachedEnvKey = k
				cachedTag = v
				return v
			}
		}
	}
	cachedEnvKey = k
	cachedTag = "en-US"
	return "en-US"
}

// commaDecimal lists language prefixes using "," as decimal separator.
// euro is the subset using the euro sign (pl/cs/hr/hu excluded).
var commaDecimal = []string{"de", "fr", "es", "it", "pt", "nl", "pl", "cs", "hr", "hu", "el", "da", "sv", "nb", "nn", "fi", "bg", "ro", "sk", "sl", "lt", "lv", "et", "ga", "mt", "is"}

var euro = []string{"de", "fr", "es", "it", "nl", "pt", "el", "da", "sv", "nb", "nn", "fi", "bg", "ro", "sk", "sl", "lt", "lv", "et", "ga", "mt", "is"}

func hasPrefixIn(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

var cachedSepTag, cachedSepDec, cachedSepGrp string
var cachedCurTag, cachedCurSym, cachedCurFull string

func Separators() (string, string) {
	tag := Tag()
	if tag == cachedSepTag && cachedSepTag != "" {
		return cachedSepDec, cachedSepGrp
	}
	dec, grp := ".", ","
	lower := strings.ToLower(tag)
	if hasPrefixIn(lower, commaDecimal) {
		dec, grp = ",", "."
		if strings.HasPrefix(lower, "fr") {
			grp = " "
		}
	}
	cachedSepTag = tag
	cachedSepDec = dec
	cachedSepGrp = grp
	return dec, grp
}

func Currency() (string, string) {
	tag := Tag()
	if tag == cachedCurTag && cachedCurTag != "" {
		return cachedCurFull, cachedCurSym
	}
	sym := "$"
	lower := strings.ToLower(tag)
	switch {
	case strings.HasPrefix(lower, "en-gb"):
		sym = "£"
	case strings.HasPrefix(lower, "ja"):
		sym = "¥"
	case strings.HasPrefix(lower, "zh"):
		sym = "¥"
	case strings.HasPrefix(lower, "ko"):
		sym = "₩"
	case strings.HasPrefix(lower, "pt-br"):
		sym = "R$"
	case hasPrefixIn(lower, euro):
		sym = "€"
	case strings.HasPrefix(lower, "en"):
		sym = "$"
	}
	cachedCurTag = tag
	cachedCurFull = tag
	cachedCurSym = sym
	return tag, sym
}
