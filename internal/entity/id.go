package entity

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// EntityID represents a parsed entity identifier.
//
//nolint:revive // EntityID is the idiomatic name even with package-name repetition; changing to "ID" would conflict with Entity.ID field.
type EntityID struct {
	Prefix string // e.g., "REQ-", "DEC-"
	Number int    // e.g., 1, 42
	Raw    string // Original string
}

// ParseEntityID parses an entity ID string into its components.
// Expected shape: PREFIX-NUMBER (e.g., REQ-001, DEC-42, ISO-CA-001).
// A hand-rolled parser is used instead of a regex because the earlier
// regex `^([A-Za-z]+(?:-[A-Za-z]+)*-?)(\d+)$` exhibited catastrophic
// backtracking on adversarial inputs.
func ParseEntityID(s string) (EntityID, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return EntityID{}, errors.New("empty entity ID")
	}

	// Walk from the end to find the trailing digit run.
	numStart := len(s)
	for numStart > 0 && s[numStart-1] >= '0' && s[numStart-1] <= '9' {
		numStart--
	}
	// No trailing digits, or all digits with no prefix — treat as opaque.
	if numStart == len(s) || numStart == 0 {
		return EntityID{Raw: s}, nil
	}

	prefix := s[:numStart]
	// Prefix must be letters with optional dash-separated letter groups,
	// optionally followed by a final dash directly before the digits.
	if !isValidIDPrefix(prefix) {
		return EntityID{Raw: s}, nil
	}

	num, err := strconv.Atoi(s[numStart:])
	if err != nil {
		return EntityID{}, fmt.Errorf("invalid number in ID: %s", s)
	}

	return EntityID{
		Prefix: prefix,
		Number: num,
		Raw:    s,
	}, nil
}

// isValidIDPrefix reports whether p matches [A-Za-z]+(-[A-Za-z]+)*-?
// without any regex backtracking.
func isValidIDPrefix(p string) bool {
	if p == "" {
		return false
	}
	// Scan char-by-char: expect a run of letters, then optionally '-' and
	// another run of letters, repeating. A single trailing '-' is allowed.
	i := 0
	for i < len(p) {
		// Letter run (must have at least one).
		start := i
		for i < len(p) && isASCIILetter(p[i]) {
			i++
		}
		if i == start {
			return false
		}
		if i == len(p) {
			return true
		}
		if p[i] != '-' {
			return false
		}
		i++ // consume the dash
		// Trailing dash is allowed only if it's the last character.
		if i == len(p) {
			return true
		}
	}
	return true // coverage-ignore: unreachable: every loop iteration either returns (empty letter-run, end-of-string,
	// or non-dash) or consumes a dash and returns when i==len(p); the loop never re-enters with i>=len(p), so this
	// final return is a defensive fallthrough
}

func isASCIILetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

// String returns the canonical string representation
func (id EntityID) String() string {
	if id.Raw != "" {
		return id.Raw
	}
	if id.Prefix != "" {
		return fmt.Sprintf("%s%d", id.Prefix, id.Number)
	}
	return strconv.Itoa(id.Number)
}

// FormatWithPadding returns the ID with zero-padded number
func (id EntityID) FormatWithPadding(width int) string {
	if id.Raw != "" && id.Prefix == "" {
		return id.Raw
	}
	return fmt.Sprintf("%s%0*d", id.Prefix, width, id.Number)
}

// NextID returns the next ID in sequence
func (id EntityID) NextID() EntityID {
	return EntityID{
		Prefix: id.Prefix,
		Number: id.Number + 1,
	}
}

// MatchesPattern checks if this ID matches a given prefix pattern
func (id EntityID) MatchesPattern(pattern string) bool {
	pattern = strings.TrimSuffix(pattern, "-")
	prefix := strings.TrimSuffix(id.Prefix, "-")
	return strings.EqualFold(prefix, pattern)
}

// idPattern is the one grammar every entity ID must satisfy: it starts with an
// ASCII letter or digit, then allows letters, digits, underscore, and hyphen.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidateID reports whether s is a valid entity ID. It is the SINGLE
// validity rule for entity IDs across the whole codebase — storeutil.ValidateID
// delegates here so no write path can reach a laxer check (TKT-IZGF7T).
//
// An ID is not just a label. It is simultaneously a filename
// (fsstore writes "<plural>/<id>.md"), a component of the relation key
// ("FROM--TYPE--TO.md"), a URL path segment, and a value that can reach an
// external command. The grammar below is the intersection of what is safe in
// all four roles, which is why it is deliberately narrower than "any string a
// filesystem would accept".
//
// Each rule earns its place:
//
//   - ASCII-only. An ID is used verbatim as a filename, and macOS stores
//     NFD while Linux stores NFC, so "café" created on one platform is a
//     *different* filename on the other — the same logical entity forks into
//     two files and git reports phantom renames. ASCII also removes homoglyph
//     ambiguity: Cyrillic "аdmin" and ASCII "admin" render identically but are
//     distinct entities. Non-English projects lose nothing: every entity type
//     has an unrestricted `title` property, which is where a human-language
//     name belongs. Identifiers stay ASCII the same way DNS labels, Kubernetes
//     resource names, and package names do.
//
//   - Must start with a letter or digit. An identifier that opens with "-" or
//     "_" is malformed on its face: it reads as an option flag ("-rf") or a
//     hidden/private marker rather than a name, and every identifier grammar
//     worth copying (DNS labels, Kubernetes names, most language identifiers)
//     says the same. This is a well-formedness rule, NOT a security control —
//     do not let it become load-bearing for command safety. Argument injection
//     is prevented structurally instead: nothing request-derived reaches a
//     command line (see internal/cmdexec and the documents renderer).
//
//   - No consecutive hyphens. "--" is the relation-key separator in
//     "FROM--TYPE--TO.md", so an ID containing it makes the relation filename
//     ambiguous to parse. This one is load-bearing for the storage format —
//     do not relax it without changing the relation key encoding.
//
//   - No path separators or "..". Path traversal, given the ID becomes a
//     filename.
//
// The character class already excludes spaces, shell metacharacters, control
// characters, NUL, and zero-width/bidi marks, so those need no separate check.
func ValidateID(s string) error {
	if s == "" {
		return errors.New("empty ID")
	}

	// The specific checks run before the character-class check so a rejection
	// names its reason ("path separator", "control character") rather than
	// falling through to a generic "invalid characters". Callers and the
	// storetest conformance suite assert on these phrases.
	if strings.ContainsAny(s, "/\\") {
		return fmt.Errorf("path separator not allowed in entity ID: %s", s)
	}

	if strings.Contains(s, "..") {
		return fmt.Errorf("path traversal not allowed in entity ID: %s", s)
	}

	for i := range len(s) {
		if s[i] < 0x20 || s[i] == 0x7f {
			return fmt.Errorf("control character not allowed in entity ID: %q", s)
		}
	}

	if strings.Contains(s, "--") {
		return fmt.Errorf("consecutive dashes not allowed in entity ID: %s", s)
	}

	if s[0] == '-' || s[0] == '_' {
		return fmt.Errorf("entity ID must start with a letter or digit: %s", s)
	}

	if !idPattern.MatchString(s) {
		return fmt.Errorf("invalid characters in entity ID: %s", s)
	}

	return nil
}

// ExtractHighestNumber finds the highest number for a given prefix from a list of IDs
func ExtractHighestNumber(ids []string, prefix string) int {
	highest := 0
	prefix = strings.ToUpper(strings.TrimSuffix(prefix, "-"))

	for _, idStr := range ids {
		id, err := ParseEntityID(idStr)
		if err != nil {
			continue
		}
		idPrefix := strings.ToUpper(strings.TrimSuffix(id.Prefix, "-"))
		if idPrefix == prefix && id.Number > highest {
			highest = id.Number
		}
	}

	return highest
}

// GenerateNextID generates the next available ID for a given prefix
func GenerateNextID(existingIDs []string, prefix string) string {
	return GenerateSequentialID(existingIDs, prefix, 0)
}

// GenerateSequentialID is [GenerateNextID] skipping skip numbers past the
// highest existing one: skip 0 yields highest+1. A negative skip counts as 0.
func GenerateSequentialID(existingIDs []string, prefix string, skip int) string {
	highest := ExtractHighestNumber(existingIDs, prefix)
	// Ensure prefix ends with dash
	if !strings.HasSuffix(prefix, "-") {
		prefix += "-"
	}
	return fmt.Sprintf("%s%03d", strings.ToUpper(prefix), highest+1+max(skip, 0))
}

// Short ID generation constants
const (
	// base36Chars contains the characters used for short ID generation
	base36Chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	// base36Size is the number of characters in base36
	base36Size = 36
	// maxShortIDLength is the maximum length of the random part of a short ID
	maxShortIDLength = 8
	// shortIDMaxAttempts is the maximum number of attempts to generate a unique ID
	shortIDMaxAttempts = 100
	// shortIDLengthIncreaseInterval is how often to increase length on collision
	shortIDLengthIncreaseInterval = 10
)

// ID length thresholds based on entity count (birthday paradox)
const (
	shortIDThreshold500   = 500   // Up to 500: use 4 chars (1.7M combinations)
	shortIDThreshold1500  = 1500  // Up to 1500: use 5 chars (60M combinations)
	shortIDThreshold10000 = 10000 // Up to 10000: use 6 chars (2.2B combinations)
	shortIDThreshold50000 = 50000 // Up to 50000: use 7 chars (78B combinations)

	shortIDLength4 = 4 // For small projects
	shortIDLength5 = 5
	shortIDLength6 = 6
	shortIDLength7 = 7
	shortIDLength8 = 8 // For large projects
)

// GenerateShortID generates a random base36 ID with the given prefix.
// Length is adaptive based on entityCount to minimize collision probability.
// The function retries with progressively longer IDs if collisions occur.
// The caps parameter controls suffix capitalization: "upper" or "lower".
//
// Precondition: prefix must satisfy metamodel.ValidateIDPrefix, which
// metamodel.Parse enforces at load time (BUG-RHFHTH) — prefixes come
// from id_prefix/id_prefixes, so every caller receives a validated
// value. A pathological prefix (e.g. "--") would otherwise flow into
// IDs that ValidateID rejects.
func GenerateShortID(existingIDs []string, prefix string, entityCount int, caps string) string {
	// Build a set for fast lookup
	existing := make(map[string]struct{}, len(existingIDs))
	for _, id := range existingIDs {
		existing[id] = struct{}{}
	}

	length := calculateIDLength(entityCount)

	for attempts := range shortIDMaxAttempts {
		id := generateRandomBase36(prefix, length, caps)
		if _, exists := existing[id]; !exists {
			return id
		}
		// Collision - increase length periodically
		if attempts > 0 && attempts%shortIDLengthIncreaseInterval == 0 && length < maxShortIDLength {
			length++
		}
	}

	// Final fallback: use maximum length
	return generateRandomBase36(prefix, maxShortIDLength, caps)
}

// calculateIDLength determines the optimal ID length based on entity count.
// Uses birthday paradox thresholds to keep collision probability low.
func calculateIDLength(entityCount int) int {
	switch {
	case entityCount <= shortIDThreshold500:
		return shortIDLength4
	case entityCount <= shortIDThreshold1500:
		return shortIDLength5
	case entityCount <= shortIDThreshold10000:
		return shortIDLength6
	case entityCount <= shortIDThreshold50000:
		return shortIDLength7
	default:
		return shortIDLength8
	}
}

// generateRandomBase36 generates a random ID with the given prefix and length.
// The caps parameter controls suffix capitalization: "upper" (default) or "lower".
func generateRandomBase36(prefix string, length int, caps string) string {
	b := make([]byte, length)
	// coverage-ignore-start: defensive: crypto/rand.Read reads from the OS CSPRNG and does not return an error on
	// supported platforms; there is
	// no injection seam (package-level crypto/rand), so this fallback is unreachable in tests
	if _, err := rand.Read(b); err != nil {
		// Fallback to less random but functional approach
		for i := range b {
			b[i] = byte(i * 7 % base36Size)
		}
	}
	// coverage-ignore-end

	for i := range b {
		b[i] = base36Chars[b[i]%base36Size]
	}

	if !strings.HasSuffix(prefix, "-") {
		prefix += "-"
	}

	suffix := string(b)
	if caps != "lower" {
		suffix = strings.ToUpper(suffix)
	}
	return prefix + suffix
}
