// Package vault stores and resolves control-host secrets.
package vault

import (
	"cmp"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"filippo.io/age"
)

var namePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var inlineRef = regexp.MustCompile(`\$\{vault:([A-Za-z_][A-Za-z0-9_]*)\}`)
var ErrInvalid = errors.New("invalid vault entry")

type MissingError struct{ Name string }

func (e *MissingError) Error() string {
	return fmt.Sprintf("vault secret %q is missing; ask the user to add it in Settings", e.Name)
}

type Metadata struct {
	Name      string    `json:"name"`
	Scope     string    `json:"scope"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type entry struct {
	Value     string    `json:"value"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type Store struct {
	dir string
	mu  sync.Mutex
}

func New(dataDir string) *Store { return &Store{dir: filepath.Join(dataDir, "vault")} }

func (s *Store) Paths(botID string) []string {
	return []string{filepath.Join(s.dir, "dex.age"), filepath.Join(s.dir, "agent-"+botID+".age")}
}

func (s *Store) Load(botID string) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := s.Paths(botID)
	wide, err := s.read(paths[0])
	if err != nil {
		return Snapshot{}, err
	}
	narrow, err := s.read(paths[1])
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(wide, narrow), nil
}

func (s *Store) Edit(botID, scope, name string, value *string) error {
	if !namePattern.MatchString(name) || (scope != "dex" && scope != "agent") || (value != nil && strings.ContainsRune(*value, 0)) {
		return ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	paths := s.Paths(botID)
	path := paths[0]
	if scope == "agent" {
		path = paths[1]
	}
	return s.edit(path, name, value)
}

// Credential reads a control-service credential. Tools and the vault UI cannot resolve these entries.
func (s *Store) Credential(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read(filepath.Join(s.dir, "credentials.age"))
	return entries[name].Value, err
}

// EditCredential changes a control-service credential outside the tool vault scopes.
func (s *Store) EditCredential(name string, value *string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.edit(filepath.Join(s.dir, "credentials.age"), name, value)
}

func (s *Store) edit(path, name string, value *string) error {
	entries, err := s.read(path)
	if err != nil {
		return err
	}
	if value == nil {
		if _, ok := entries[name]; !ok {
			return nil
		}
		delete(entries, name)
	} else {
		entries[name] = entry{Value: *value, UpdatedAt: time.Now().UTC()}
	}
	return s.write(path, entries)
}

// MCPGrant stores credentials apart from the user vault and its reference resolver.
func (s *Store) MCPGrant(key [32]byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := s.read(s.mcpPath(key))
	return entries["grant"].Value, err
}

func (s *Store) SaveMCPGrant(key [32]byte, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(s.mcpPath(key), map[string]entry{"grant": {Value: value, UpdatedAt: time.Now().UTC()}})
}

func (s *Store) mcpPath(key [32]byte) string {
	return filepath.Join(s.dir, "mcp-"+hex.EncodeToString(key[:])+".age")
}

// Exact reads a secret at one level. Bot entries cannot change shared clients.
func (s *Store) Exact(botID, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.Paths(botID)[0]
	if botID != "" {
		path = s.Paths(botID)[1]
	}
	entries, err := s.read(path)
	if err != nil {
		return "", err
	}
	if e, ok := entries[name]; ok && e.Value != "" {
		return e.Value, nil
	}
	return "", &MissingError{Name: name}
}

func (s *Store) write(path string, entries map[string]entry) error {
	identity, err := s.identity(true)
	if err != nil {
		return err
	}
	return atomicFile(path, func(w io.Writer) error {
		out, err := age.Encrypt(w, identity.Recipient())
		if err != nil {
			return err
		}
		return errors.Join(json.MarshalWrite(out, entries), out.Close())
	})
}

func (s *Store) read(path string) (map[string]entry, error) {
	entries := map[string]entry{}
	file, err := os.Open(path) //nolint:gosec // The store selects vault paths.
	if errors.Is(err, os.ErrNotExist) {
		return entries, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	identity, err := s.identity(false)
	if err != nil {
		return nil, err
	}
	plain, err := age.Decrypt(file, identity)
	if err != nil {
		return nil, fmt.Errorf("decrypt vault: %w", err)
	}
	if err := json.UnmarshalRead(plain, &entries); err != nil {
		return nil, errors.New("invalid encrypted vault data")
	}
	return entries, nil
}

func (s *Store) identity(create bool) (*age.X25519Identity, error) {
	path := filepath.Join(s.dir, "identity.txt")
	text, err := os.ReadFile(path) //nolint:gosec // Fixed identity path.
	if err == nil {
		identity, err := age.ParseX25519Identity(strings.TrimSpace(string(text)))
		if err != nil {
			return nil, errors.New("invalid vault identity")
		}
		return identity, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if !create {
		return nil, errors.New("vault identity is missing")
	}
	// Do not generate a replacement key for a copy with encrypted values.
	files, err := os.ReadDir(s.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, file := range files {
		if strings.HasSuffix(file.Name(), ".age") {
			return s.identity(false)
		}
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	err = writeTempFile(path, func(w io.Writer) error {
		_, err := io.WriteString(w, identity.String()+"\n")
		return err
	}, os.Link)
	if errors.Is(err, os.ErrExist) {
		return s.identity(false)
	}
	return identity, err
}

func atomicFile(path string, write func(io.Writer) error) error {
	return writeTempFile(path, write, os.Rename)
}

func writeTempFile(path string, write func(io.Writer) error, publish func(string, string) error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".vault-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := errors.Join(write(file), file.Close()); err != nil {
		return err
	}
	return publish(file.Name(), path)
}

type Snapshot struct {
	values map[string]entry
	list   []Metadata
	all    []Metadata
	mask   *strings.Replacer
}

func snapshot(wide, narrow map[string]entry) Snapshot {
	values := maps.Clone(wide)
	maps.Copy(values, narrow)
	names := slices.Sorted(maps.Keys(values))
	list := make([]Metadata, 0, len(names))
	for _, name := range names {
		scope := "dex"
		if _, ok := narrow[name]; ok {
			scope = "agent"
		}
		list = append(list, Metadata{Name: name, Scope: scope, UpdatedAt: values[name].UpdatedAt})
	}
	// Match the longest value first when one value is a prefix of another.
	slices.SortStableFunc(names, func(a, b string) int { return cmp.Compare(len(values[b].Value), len(values[a].Value)) })
	pairs := []string{}
	for _, name := range names {
		if value := values[name].Value; utf8.RuneCountInString(value) >= 8 {
			pairs = append(pairs, value, "[vault:"+name+"]")
		}
	}
	all := make([]Metadata, 0, len(wide)+len(narrow))
	for _, name := range slices.Sorted(maps.Keys(wide)) {
		all = append(all, Metadata{Name: name, Scope: "dex", UpdatedAt: wide[name].UpdatedAt})
	}
	for _, name := range slices.Sorted(maps.Keys(narrow)) {
		all = append(all, Metadata{Name: name, Scope: "agent", UpdatedAt: narrow[name].UpdatedAt})
	}
	var mask *strings.Replacer
	if len(pairs) != 0 {
		mask = strings.NewReplacer(pairs...)
	}
	return Snapshot{values: values, list: list, all: all, mask: mask}
}

func (s Snapshot) List() []Metadata {
	if s.list == nil {
		return []Metadata{}
	}
	return slices.Clone(s.list)
}

// All lists both levels, including values that the bot level overrides.
func (s Snapshot) All() []Metadata { return slices.Clone(s.all) }

func (s Snapshot) HasMask() bool { return s.mask != nil }

func (s Snapshot) Mask(text string) string {
	if s.mask == nil {
		return text
	}
	return s.mask.Replace(text)
}

func (s Snapshot) Resolve(env map[string]string, interpolate bool) (map[string]string, error) {
	lookup := func(name string) (string, error) {
		e, ok := s.values[name]
		if !ok {
			return "", &MissingError{Name: name}
		}
		return e.Value, nil
	}
	resolved := maps.Clone(env)
	for key, text := range env {
		if name, ok := strings.CutPrefix(text, "vault:"); ok {
			value, err := lookup(name)
			if err != nil {
				return nil, err
			}
			resolved[key] = value
		} else if interpolate {
			var err error
			resolved[key] = inlineRef.ReplaceAllStringFunc(text, func(ref string) string {
				if err != nil {
					return ""
				}
				var value string
				value, err = lookup(ref[len("${vault:") : len(ref)-1])
				return value
			})
			if err != nil {
				return nil, err
			}
		}
	}
	return resolved, nil
}
