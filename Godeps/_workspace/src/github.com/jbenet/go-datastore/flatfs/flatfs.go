// Package flatfs is a Datastore implementation that stores all
// objects in a two-level directory structure in the local file
// system, regardless of the hierarchy of the keys.
package flatfs

import (
	"encoding/hex"
	"errors"
	"io/ioutil"
	"os"
	"path"
	"strings"

	"github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore"
	"github.com/ipfs/go-ipfs/Godeps/_workspace/src/github.com/jbenet/go-datastore/query"
)

const (
	extension    = ".data"
	maxPrefixLen = 16
)

var (
	ErrBadPrefixLen = errors.New("bad prefix length")
)

type Datastore struct {
	path string
	// length of the dir splay prefix, in bytes of hex digits
	hexPrefixLen int
}

var _ datastore.Datastore = (*Datastore)(nil)

func New(path string, prefixLen int) (*Datastore, error) {
	if prefixLen <= 0 || prefixLen > maxPrefixLen {
		return nil, ErrBadPrefixLen
	}
	fs := &Datastore{
		path: path,
		// convert from binary bytes to bytes of hex encoding
		hexPrefixLen: prefixLen * hex.EncodedLen(1),
	}
	return fs, nil
}

var padding = strings.Repeat("_", maxPrefixLen*hex.EncodedLen(1))

func (fs *Datastore) encode(key datastore.Key) (dir, file string) {
	safe := hex.EncodeToString(key.Bytes())
	prefix := (safe + padding)[:fs.hexPrefixLen]
	dir = path.Join(fs.path, prefix)
	file = path.Join(dir, safe+extension)
	return dir, file
}

func (fs *Datastore) decode(file string) (key datastore.Key, ok bool) {
	if path.Ext(file) != extension {
		return datastore.Key{}, false
	}
	name := file[:len(file)-len(extension)]
	k, err := hex.DecodeString(name)
	if err != nil {
		return datastore.Key{}, false
	}
	return datastore.NewKey(string(k)), true
}

func (fs *Datastore) makePrefixDir(dir string) error {
	if err := os.Mkdir(dir, 0777); err != nil {
		// EEXIST is safe to ignore here, that just means the prefix
		// directory already existed.
		if !os.IsExist(err) {
			return err
		}
	}

	// In theory, if we create a new prefix dir and add a file to
	// it, the creation of the prefix dir itself might not be
	// durable yet. Sync the root dir after a successful mkdir of
	// a prefix dir, just to be paranoid.
	f, err := os.Open(fs.path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return err
	}
	return nil
}

func (fs *Datastore) Put(key datastore.Key, value interface{}) error {
	val, ok := value.([]byte)
	if !ok {
		return datastore.ErrInvalidType
	}

	dir, path := fs.encode(key)
	if err := fs.makePrefixDir(dir); err != nil {
		return err
	}

	dirF, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer dirF.Close()

	tmp, err := ioutil.TempFile(dir, "put-")
	if err != nil {
		return err
	}
	closed := false
	removed := false
	defer func() {
		if !closed {
			// silence errcheck
			_ = tmp.Close()
		}
		if !removed {
			// silence errcheck
			_ = os.Remove(tmp.Name())
		}
	}()

	if _, err := tmp.Write(val); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	closed = true

	err = os.Rename(tmp.Name(), path)
	if err != nil {
		return err
	}
	removed = true

	if err := dirF.Sync(); err != nil {
		return err
	}

	return nil
}

func (fs *Datastore) Get(key datastore.Key) (value interface{}, err error) {
	_, path := fs.encode(key)
	data, err := ioutil.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, datastore.ErrNotFound
		}
		// no specific error to return, so just pass it through
		return nil, err
	}
	return data, nil
}

func (fs *Datastore) Has(key datastore.Key) (exists bool, err error) {
	_, path := fs.encode(key)
	switch _, err := os.Stat(path); {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func (fs *Datastore) Delete(key datastore.Key) error {
	_, path := fs.encode(key)
	switch err := os.Remove(path); {
	case err == nil:
		return nil
	case os.IsNotExist(err):
		return datastore.ErrNotFound
	default:
		return err
	}
}

func (fs *Datastore) Query(q query.Query) (query.Results, error) {
	if (q.Prefix != "" && q.Prefix != "/") ||
		len(q.Filters) > 0 ||
		len(q.Orders) > 0 ||
		q.Limit > 0 ||
		q.Offset > 0 ||
		!q.KeysOnly {
		// TODO this is overly simplistic, but the only caller is
		// `ipfs refs local` for now, and this gets us moving.
		return nil, errors.New("flatfs only supports listing all keys in random order")
	}

	// TODO this dumb implementation gathers all keys into a single slice.
	root, err := os.Open(fs.path)
	if err != nil {
		return nil, err
	}
	defer root.Close()

	var res []query.Entry
	prefixes, err := root.Readdir(0)
	if err != nil {
		return nil, err
	}
	for _, fi := range prefixes {
		if !fi.IsDir() || fi.Name()[0] == '.' {
			continue
		}
		child, err := os.Open(path.Join(fs.path, fi.Name()))
		if err != nil {
			return nil, err
		}
		defer child.Close()
		objs, err := child.Readdir(0)
		if err != nil {
			return nil, err
		}
		for _, fi := range objs {
			if !fi.Mode().IsRegular() || fi.Name()[0] == '.' {
				continue
			}
			key, ok := fs.decode(fi.Name())
			if !ok {
				continue
			}
			res = append(res, query.Entry{Key: key.String()})
		}
	}
	return query.ResultsWithEntries(q, res), nil
}

var _ datastore.ThreadSafeDatastore = (*Datastore)(nil)

func (*Datastore) IsThreadSafe() {}
