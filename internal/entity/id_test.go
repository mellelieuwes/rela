package entity

import (
	"strings"
	"testing"
)

// assertEqualID is a local helper (entity_test.go uses testify but this
// internal test file uses the in-package t.Helper style like the original model tests).
func assertEqualID(t *testing.T, got, want any) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestEntityIDString tests String method with various scenarios
func TestEntityIDString(t *testing.T) {
	tests := []struct {
		name     string
		id       EntityID
		expected string
	}{
		{
			name:     "with raw",
			id:       EntityID{Raw: "RAW-ID"},
			expected: "RAW-ID",
		},
		{
			name:     "with prefix and number",
			id:       EntityID{Prefix: "REQ-", Number: 42},
			expected: "REQ-42",
		},
		{
			name:     "number only",
			id:       EntityID{Number: 42},
			expected: "42",
		},
		{
			name:     "empty",
			id:       EntityID{},
			expected: "0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.String(); got != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, got)
			}
		})
	}
}

// TestEntityIDNextID tests NextID generation
func TestEntityIDNextID(t *testing.T) {
	id := EntityID{Prefix: "REQ-", Number: 42}
	next := id.NextID()

	if next.Prefix != "REQ-" {
		t.Errorf("expected prefix REQ-, got %s", next.Prefix)
	}
	if next.Number != 43 {
		t.Errorf("expected number 43, got %d", next.Number)
	}
}

// TestEntityIDMatchesPattern tests pattern matching
func TestEntityIDMatchesPattern(t *testing.T) {
	tests := []struct {
		name     string
		id       EntityID
		pattern  string
		expected bool
	}{
		{
			name:     "exact match with dash",
			id:       EntityID{Prefix: "REQ-"},
			pattern:  "REQ-",
			expected: true,
		},
		{
			name:     "exact match without dash",
			id:       EntityID{Prefix: "REQ-"},
			pattern:  "REQ",
			expected: true,
		},
		{
			name:     "case insensitive match",
			id:       EntityID{Prefix: "REQ-"},
			pattern:  "req",
			expected: true,
		},
		{
			name:     "no match",
			id:       EntityID{Prefix: "REQ-"},
			pattern:  "DEC",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.MatchesPattern(tt.pattern); got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

// TestParseEntityID_MultiSegmentPrefix tests parsing IDs with multi-segment prefixes
func TestParseEntityID_MultiSegmentPrefix(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantPrefix string
		wantNumber int
		wantRaw    string
	}{
		{
			name:       "single segment prefix",
			input:      "REQ-001",
			wantPrefix: "REQ-",
			wantNumber: 1,
			wantRaw:    "REQ-001",
		},
		{
			name:       "two segment prefix",
			input:      "ISO-CA-001",
			wantPrefix: "ISO-CA-",
			wantNumber: 1,
			wantRaw:    "ISO-CA-001",
		},
		{
			name:       "three segment prefix",
			input:      "ISO-CA-XX-042",
			wantPrefix: "ISO-CA-XX-",
			wantNumber: 42,
			wantRaw:    "ISO-CA-XX-042",
		},
		{
			name:       "two segment no trailing dash",
			input:      "ISO-CA001",
			wantPrefix: "ISO-CA",
			wantNumber: 1,
			wantRaw:    "ISO-CA001",
		},
		{
			name:       "single letter segments",
			input:      "A-B-1",
			wantPrefix: "A-B-",
			wantNumber: 1,
			wantRaw:    "A-B-1",
		},
		{
			name:    "opaque id with no number",
			input:   "PERS-JV",
			wantRaw: "PERS-JV",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := ParseEntityID(tt.input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			assertEqualID(t, id.Prefix, tt.wantPrefix)
			assertEqualID(t, id.Number, tt.wantNumber)
			assertEqualID(t, id.Raw, tt.wantRaw)
		})
	}
}

// TestExtractHighestNumber_MultiSegmentPrefix tests number extraction with multi-segment prefixes
func TestExtractHighestNumber_MultiSegmentPrefix(t *testing.T) {
	tests := []struct {
		name   string
		ids    []string
		prefix string
		want   int
	}{
		{
			name:   "single segment",
			ids:    []string{"REQ-001", "REQ-002", "REQ-003"},
			prefix: "REQ-",
			want:   3,
		},
		{
			name:   "multi segment ISO-CA",
			ids:    []string{"ISO-CA-001", "ISO-CA-002", "ISO-CA-010"},
			prefix: "ISO-CA-",
			want:   10,
		},
		{
			name:   "mixed with other prefixes",
			ids:    []string{"ISO-CA-001", "REQ-005", "ISO-CA-003"},
			prefix: "ISO-CA-",
			want:   3,
		},
		{
			name:   "no matches",
			ids:    []string{"REQ-001", "DEC-002"},
			prefix: "ISO-CA-",
			want:   0,
		},
		{
			name:   "empty list",
			ids:    []string{},
			prefix: "ISO-CA-",
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractHighestNumber(tt.ids, tt.prefix)
			assertEqualID(t, got, tt.want)
		})
	}
}

// TestGenerateNextID_MultiSegmentPrefix tests ID generation with multi-segment prefixes
func TestGenerateNextID_MultiSegmentPrefix(t *testing.T) {
	tests := []struct {
		name   string
		ids    []string
		prefix string
		want   string
	}{
		{
			name:   "first ISO-CA ID",
			ids:    []string{},
			prefix: "ISO-CA-",
			want:   "ISO-CA-001",
		},
		{
			name:   "next ISO-CA ID",
			ids:    []string{"ISO-CA-001"},
			prefix: "ISO-CA-",
			want:   "ISO-CA-002",
		},
		{
			name:   "gap in sequence",
			ids:    []string{"ISO-CA-001", "ISO-CA-005"},
			prefix: "ISO-CA-",
			want:   "ISO-CA-006",
		},
		{
			name:   "single segment still works",
			ids:    []string{"REQ-001", "REQ-002"},
			prefix: "REQ-",
			want:   "REQ-003",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateNextID(tt.ids, tt.prefix)
			assertEqualID(t, got, tt.want)
		})
	}
}

// TestEntityIDMatchesPattern_MultiSegment tests pattern matching with multi-segment prefixes
func TestEntityIDMatchesPattern_MultiSegment(t *testing.T) {
	tests := []struct {
		name     string
		id       EntityID
		pattern  string
		expected bool
	}{
		{
			name:     "multi segment exact",
			id:       EntityID{Prefix: "ISO-CA-"},
			pattern:  "ISO-CA-",
			expected: true,
		},
		{
			name:     "multi segment without trailing dash",
			id:       EntityID{Prefix: "ISO-CA-"},
			pattern:  "ISO-CA",
			expected: true,
		},
		{
			name:     "multi segment case insensitive",
			id:       EntityID{Prefix: "ISO-CA-"},
			pattern:  "iso-ca",
			expected: true,
		},
		{
			name:     "multi segment no match",
			id:       EntityID{Prefix: "ISO-CA-"},
			pattern:  "ISO-CB",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.id.MatchesPattern(tt.pattern); got != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, got)
			}
		})
	}
}

// TestGenerateShortID tests short random ID generation
func TestGenerateShortID(t *testing.T) {
	tests := []struct {
		name        string
		existingIDs []string
		prefix      string
		entityCount int
		caps        string
		wantPrefix  string
		wantLength  int
		wantUpper   bool
	}{
		{
			name:        "first ID with empty prefix uppercase",
			existingIDs: []string{},
			prefix:      "TKT",
			entityCount: 0,
			caps:        "upper",
			wantPrefix:  "TKT-",
			wantLength:  4, // 4 chars for small counts
			wantUpper:   true,
		},
		{
			name:        "prefix with trailing dash",
			existingIDs: []string{},
			prefix:      "REQ-",
			entityCount: 0,
			caps:        "upper",
			wantPrefix:  "REQ-",
			wantLength:  4,
			wantUpper:   true,
		},
		{
			name:        "lowercase suffix",
			existingIDs: []string{},
			prefix:      "TKT-",
			entityCount: 0,
			caps:        "lower",
			wantPrefix:  "TKT-",
			wantLength:  4,
			wantUpper:   false,
		},
		{
			name:        "prefix case preserved",
			existingIDs: []string{},
			prefix:      "MyType-",
			entityCount: 0,
			caps:        "upper",
			wantPrefix:  "MyType-",
			wantLength:  4,
			wantUpper:   true,
		},
		{
			name:        "length scales with entity count 501",
			existingIDs: []string{},
			prefix:      "TKT",
			entityCount: 501,
			caps:        "upper",
			wantPrefix:  "TKT-",
			wantLength:  5,
			wantUpper:   true,
		},
		{
			name:        "length scales with entity count 1501",
			existingIDs: []string{},
			prefix:      "TKT",
			entityCount: 1501,
			caps:        "upper",
			wantPrefix:  "TKT-",
			wantLength:  6,
			wantUpper:   true,
		},
		{
			name:        "length scales with entity count 10001",
			existingIDs: []string{},
			prefix:      "TKT",
			entityCount: 10001,
			caps:        "upper",
			wantPrefix:  "TKT-",
			wantLength:  7,
			wantUpper:   true,
		},
		{
			name:        "length scales with entity count 50001",
			existingIDs: []string{},
			prefix:      "TKT",
			entityCount: 50001,
			caps:        "upper",
			wantPrefix:  "TKT-",
			wantLength:  8,
			wantUpper:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GenerateShortID(tt.existingIDs, tt.prefix, tt.entityCount, tt.caps)

			// Check prefix
			if len(got) < len(tt.wantPrefix) || got[:len(tt.wantPrefix)] != tt.wantPrefix {
				t.Errorf("GenerateShortID() = %q, want prefix %q", got, tt.wantPrefix)
			}

			// Check length (prefix + random part)
			wantTotalLen := len(tt.wantPrefix) + tt.wantLength
			if len(got) != wantTotalLen {
				t.Errorf("GenerateShortID() length = %d, want %d (got %q)", len(got), wantTotalLen, got)
			}

			// Check random part contains only valid base36 characters with correct case
			randomPart := got[len(tt.wantPrefix):]
			for _, c := range randomPart {
				if tt.wantUpper {
					if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
						t.Errorf("GenerateShortID() random part contains invalid char %q in %q (expected uppercase)", c, got)
					}
				} else {
					if (c < '0' || c > '9') && (c < 'a' || c > 'z') {
						t.Errorf("GenerateShortID() random part contains invalid char %q in %q (expected lowercase)", c, got)
					}
				}
			}
		})
	}
}

// TestGenerateShortID_Uniqueness tests that generated IDs are unique
func TestGenerateShortID_Uniqueness(t *testing.T) {
	generated := make(map[string]bool)
	existingIDs := []string{}

	// Generate many IDs and check for uniqueness
	for i := range 1000 {
		id := GenerateShortID(existingIDs, "TKT", i, "upper")
		if generated[id] {
			t.Errorf("Duplicate ID generated: %s", id)
		}
		generated[id] = true
		existingIDs = append(existingIDs, id)
	}
}

// TestGenerateShortID_CollisionAvoidance tests collision handling
func TestGenerateShortID_CollisionAvoidance(t *testing.T) {
	// Pre-populate with some IDs that might collide
	existingIDs := []string{"TKT-0000", "TKT-1111", "TKT-AAAA"}

	// Generate IDs and ensure none collide with existing
	for range 100 {
		id := GenerateShortID(existingIDs, "TKT", len(existingIDs), "upper")
		for _, existing := range existingIDs {
			if id == existing {
				t.Errorf("Generated ID %s collides with existing ID", id)
			}
		}
		existingIDs = append(existingIDs, id)
	}
}

// TestCalculateIDLength tests the ID length calculation
func TestCalculateIDLength(t *testing.T) {
	tests := []struct {
		entityCount int
		wantLength  int
	}{
		{0, 4},
		{500, 4},
		{501, 5},
		{1500, 5},
		{1501, 6},
		{10000, 6},
		{10001, 7},
		{50000, 7},
		{50001, 8},
		{100000, 8},
	}

	for _, tt := range tests {
		got := calculateIDLength(tt.entityCount)
		if got != tt.wantLength {
			t.Errorf("calculateIDLength(%d) = %d, want %d", tt.entityCount, got, tt.wantLength)
		}
	}
}

// TestValidateID pins the single ID grammar. See the ValidateID godoc for
// why each rule exists; this test exists so a future "simplification" of the
// character class fails loudly rather than silently widening what can become
// a filename, a relation-key component, or a command-line argument.
func TestValidateID(t *testing.T) {
	valid := []string{
		"TKT-001",
		"FOO-BAR",
		"a",
		"ai-integration",    // the manual-ID slug convention
		"data_entry_server", // underscores are allowed
		"A1",
	}
	for _, id := range valid {
		t.Run("valid/"+id, func(t *testing.T) {
			if err := ValidateID(id); err != nil {
				t.Errorf("ValidateID(%q) = %v, want nil", id, err)
			}
		})
	}

	invalid := []struct {
		name string
		id   string
		want string // substring the message must name
	}{
		{"empty", "", "empty ID"},
		{"forward slash", "foo/bar", "path separator"},
		{"backslash", "foo\\bar", "path separator"},
		{"traversal", "..", "path traversal"},
		{"NUL", "foo\x00bar", "control character"},
		{"newline", "foo\nbar", "control character"},
		{"tab", "foo\tbar", "control character"},
		{"DEL", "foo\x7fbar", "control character"},
		{"consecutive dashes", "FOO--BAR", "consecutive dashes"},

		// An identifier must open with a letter or digit; "-" reads as an
		// option flag and "_" as a hidden/private marker.
		{"leading dash", "-rf", "must start with a letter or digit"},
		{"leading dash word", "-oevil", "must start with a letter or digit"},
		{"leading underscore", "_private", "must start with a letter or digit"},

		// ASCII-only: NFC/NFD divergence across platforms, plus homoglyphs.
		{"latin accent", "café", "invalid characters"},
		{"cjk", "日本語", "invalid characters"},
		{"cyrillic homoglyph", "аdmin", "invalid characters"},
		{"fullwidth", "ＴＫＴ", "invalid characters"},
		{"zero width space", "a\u200bb", "invalid characters"},
		{"bidi override", "a\u202eb", "invalid characters"},

		// Shell-hostile characters, excluded by the character class.
		{"space", "a b", "invalid characters"},
		{"semicolon", "a;b", "invalid characters"},
		{"command substitution", "a$(id)", "invalid characters"},
		{"backtick", "a`id`", "invalid characters"},
		{"glob", "a*b", "invalid characters"},
		{"quote", "a'b", "invalid characters"},
		{"leading dot", ".git", "invalid characters"},
	}
	for _, tt := range invalid {
		t.Run("invalid/"+tt.name, func(t *testing.T) {
			err := ValidateID(tt.id)
			if err == nil {
				t.Fatalf("ValidateID(%q) = nil, want error", tt.id)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("ValidateID(%q) = %q, want message containing %q", tt.id, err, tt.want)
			}
		})
	}
}

// TestValidateID_RejectsReservedPrincipalSeparator pins a property that
// internal/dataentry's reserved-principal guard (TKT-9PCL7D) silently depends
// on: an entity ID can never contain ':'.
//
// The dependency is indirect, which is exactly why it needs a test here. When
// acl.yaml sets `principal_property`, resolvePrincipalEntity SUBSTITUTES the
// acting Principal.User with the resolved entity's ID — after the boundary
// check that rejects `system:`-prefixed names has already run. So the ID
// becomes an acting identity that no longer passes through that guard.
//
// Today that is safe only because this grammar forbids ':', making it
// impossible to author an entity whose ID is `system:scheduler`. If the pattern
// were ever widened to admit ':', such an entity would be stamped as the acting
// principal and inherit whatever acl.yaml grants that name — which, after the
// DEC-O59WM4 migration, is `read: ["*"]`.
//
// Widening the grammar is not forbidden; doing so without revisiting
// internal/dataentry/router.go's resolvePrincipalEntity is.
func TestValidateID_RejectsReservedPrincipalSeparator(t *testing.T) {
	for _, id := range []string{
		"system:scheduler",
		"system:provisioner",
		"PERS-1:2",
		"a:b",
	} {
		if err := ValidateID(id); err == nil {
			t.Errorf("ValidateID(%q) = nil, want an error — an ID containing ':' "+
				"could be substituted as an acting principal and collide with a "+
				"reserved system: identity (TKT-9PCL7D)", id)
		}
	}
}

func TestGenerateSequentialID(t *testing.T) {
	ids := []string{"TKT-001", "TKT-004"}
	tests := []struct {
		name string
		skip int
		want string
	}{
		{"skip zero is next", 0, "TKT-005"},
		{"skip spreads past next", 3, "TKT-008"},
		{"negative skip counts as zero", -2, "TKT-005"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := GenerateSequentialID(ids, "TKT-", tc.skip); got != tc.want {
				t.Errorf("GenerateSequentialID(skip=%d) = %s, want %s", tc.skip, got, tc.want)
			}
		})
	}
}
