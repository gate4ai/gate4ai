// SPDX-License-Identifier: FSL-1.1-ALv2

package audit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
)

// Manifest — опись архива экспорта; подписывается ключом устройства.
type Manifest struct {
	Format    string            `json:"format"`
	Created   time.Time         `json:"created"`
	PublicKey string            `json:"public_key"`
	Files     map[string]string `json:"files"` // имя → sha256
}

// Export пишет подписанный архив (tar.gz): квитанции, чекпоинты, доказательства
// (расшифрованные, чтобы их хэши проверялись где угодно), публичный ключ.
// Если passphrase не пуст, весь архив шифруется age (scrypt).
func (j *Journal) Export(w io.Writer, passphrase string) error {
	entries, err := j.List(Filter{})
	if err != nil {
		return err
	}
	cps, err := j.Checkpoints()
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	var rb bytes.Buffer
	for _, e := range entries {
		b, _ := json.Marshal(e)
		rb.Write(b)
		rb.WriteByte('\n')
		for _, ref := range e.Receipt.Verification.EvidenceRefs {
			data, err := j.Evidence.Get(ref)
			if errors.Is(err, ErrShredded) {
				continue
			}
			if err != nil {
				return err
			}
			files["evidence/"+strings.TrimPrefix(ref, "sha256:")] = data
		}
	}
	files["receipts.jsonl"] = rb.Bytes()
	var cb bytes.Buffer
	for _, c := range cps {
		b, _ := json.Marshal(c)
		cb.Write(b)
		cb.WriteByte('\n')
	}
	files["checkpoints.jsonl"] = cb.Bytes()
	pub := hex.EncodeToString(j.PublicKey())
	files["public_key.txt"] = []byte(pub + "\n")
	m := Manifest{Format: "gate4ai-export/1", Created: j.now().UTC(), PublicKey: pub, Files: map[string]string{}}
	for n, b := range files {
		m.Files[n] = HashBytes(b)
	}
	mb, err := Canonicalize(m)
	if err != nil {
		return err
	}
	files["manifest.json"] = mb
	files["manifest.sig"] = []byte(hex.EncodeToString(ed25519.Sign(j.priv, mb)) + "\n")

	out := w
	var aw io.WriteCloser
	if passphrase != "" {
		r, err := age.NewScryptRecipient(passphrase)
		if err != nil {
			return err
		}
		if aw, err = age.Encrypt(w, r); err != nil {
			return err
		}
		out = aw
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o600, Size: int64(len(files[n])), ModTime: m.Created}); err != nil {
			return err
		}
		if _, err := tw.Write(files[n]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if aw != nil {
		return aw.Close()
	}
	return nil
}

// VerifyReport — результат проверки архива.
type VerifyReport struct {
	Receipts        int      `json:"receipts"`
	Redacted        int      `json:"redacted"`
	Checkpoints     int      `json:"checkpoints"`
	EvidenceOK      int      `json:"evidence_ok"`
	EvidenceDeleted int      `json:"evidence_deleted"`
	PublicKey       string   `json:"public_key"`
	Errors          []string `json:"errors,omitempty"`
}

// OK — архив цел.
func (r VerifyReport) OK() bool { return len(r.Errors) == 0 }

// VerifyArchive проверяет архив экспорта без доступа к устройству: подпись описи,
// хэши файлов, цепочку квитанций, подписи чекпоинтов и хэши доказательств.
// expectPub (hex) — ожидаемый ключ устройства; пустой — доверять ключу из архива.
func VerifyArchive(r io.Reader, passphrase, expectPub string) (VerifyReport, error) {
	var rep VerifyReport
	if passphrase != "" {
		id, err := age.NewScryptIdentity(passphrase)
		if err != nil {
			return rep, err
		}
		if r, err = age.Decrypt(r, id); err != nil {
			return rep, fmt.Errorf("decrypt archive: %w", err)
		}
	}
	gz, err := gzip.NewReader(r)
	if err != nil {
		return rep, fmt.Errorf("not a gate4ai export: %w", err)
	}
	tr := tar.NewReader(gz)
	files := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return rep, err
		}
		b, err := io.ReadAll(io.LimitReader(tr, 1<<30))
		if err != nil {
			return rep, err
		}
		files[h.Name] = b
	}
	fail := func(f string, a ...any) { rep.Errors = append(rep.Errors, fmt.Sprintf(f, a...)) }
	mb := files["manifest.json"]
	var m Manifest
	if err := json.Unmarshal(mb, &m); err != nil {
		return rep, fmt.Errorf("manifest: %w", err)
	}
	rep.PublicKey = m.PublicKey
	pub, err := hex.DecodeString(m.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return rep, errors.New("manifest: bad public key")
	}
	if expectPub != "" && !strings.EqualFold(expectPub, m.PublicKey) {
		fail("public key %s does not match expected %s", m.PublicKey, expectPub)
	}
	sig, _ := hex.DecodeString(strings.TrimSpace(string(files["manifest.sig"])))
	if !ed25519.Verify(pub, mb, sig) {
		fail("manifest signature invalid")
	}
	for n, sum := range m.Files {
		b, ok := files[n]
		if !ok {
			fail("missing file %s", n)
			continue
		}
		if HashBytes(b) != sum {
			fail("file %s hash mismatch", n)
		}
	}
	for n := range files {
		if _, ok := m.Files[n]; !ok && n != "manifest.json" && n != "manifest.sig" {
			fail("unlisted file %s", n)
		}
	}
	var entries []Entry
	for _, line := range bytes.Split(files["receipts.jsonl"], []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			fail("receipt parse: %v", err)
			continue
		}
		entries = append(entries, e)
		if e.Redacted {
			rep.Redacted++
		}
		for _, ref := range e.Receipt.Verification.EvidenceRefs {
			b, ok := files["evidence/"+strings.TrimPrefix(ref, "sha256:")]
			switch {
			case !ok:
				rep.EvidenceDeleted++
			case Ref(HashBytes(b)) != ref:
				fail("evidence %s hash mismatch", ref)
			default:
				rep.EvidenceOK++
			}
		}
	}
	rep.Receipts = len(entries)
	var cps []Checkpoint
	for _, line := range bytes.Split(files["checkpoints.jsonl"], []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var c Checkpoint
		if err := json.Unmarshal(line, &c); err != nil {
			fail("checkpoint parse: %v", err)
			continue
		}
		cps = append(cps, c)
	}
	rep.Checkpoints = len(cps)
	if err := verifyChain(entries, cps, pub); err != nil {
		fail("%v", err)
	}
	return rep, nil
}
