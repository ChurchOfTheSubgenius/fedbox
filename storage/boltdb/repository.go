package boltdb

import (
	"bytes"
	"fmt"
	"github.com/boltdb/bolt"
	as "github.com/go-ap/activitystreams"
	"github.com/go-ap/errors"
	ap "github.com/go-ap/fedbox/activitypub"
	"github.com/go-ap/jsonld"
	s "github.com/go-ap/storage"
	"github.com/pborman/uuid"
	"strings"
)

type boltDB struct {
	d       *bolt.DB
	baseURL string
	root    []byte
	path    string
	logFn   loggerFn
	errFn   loggerFn
}

type loggerFn func(string, ...interface{})

const (
	bucketActors     = "actors"
	bucketActivities = "activities"
	bucketObjects    = "objects"
)

// Config
type Config struct {
	Path       string
	BucketName string
	LogFn      loggerFn
	ErrFn      loggerFn
}

// New returns a new boltDB repository
func New(c Config, baseURL string) (*boltDB, error) {
	b := boltDB{
		root:  []byte(c.BucketName),
		path:  c.Path,
		baseURL: baseURL,
		logFn: func(string, ...interface{}) {},
		errFn: func(string, ...interface{}) {},
	}
	if c.ErrFn != nil {
		b.errFn = c.ErrFn
	}
	if c.LogFn != nil {
		b.logFn = c.LogFn
	}
	return &b, nil
}

func loadFromBucket(db *bolt.DB, root, bucket []byte, f s.Filterable) (as.ItemCollection, uint, error) {
	col := make(as.ItemCollection, 0)

	err := db.View(func(tx *bolt.Tx) error {
		rb := tx.Bucket(root)
		if rb == nil {
			return errors.Errorf("Invalid bucket %s", root)
		}
		iri := f.ID()
		url, err := iri.URL()
		if err != nil {
			return errors.Newf("invalid IRI filter element %s when loading collections", iri)
		}
		if string(root) != url.Host {
			return errors.Newf("trying to load from non-local root bucket %s", url.Host)
		}
		// Assume bucket exists and has keys
		b, path, err := descendInBucket(rb, url.Path)
		if err != nil {
			return err
		}
		if b == nil {
			return errors.Errorf("Invalid bucket %s.%s", root, bucket)
		}

		c := b.Cursor()
		if c == nil {
			return errors.Errorf("Invalid bucket cursor %s.%s", root, bucket)
		}
		prefix := []byte(path)
		for k, v := c.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, v = c.Next() {
			if it, err := as.UnmarshalJSON(v); err == nil {
				col = append(col, it)
			}
		}
		return nil
	})

	return col, uint(len(col)), err
}

// Load
func (b *boltDB) Load(f s.Filterable) (as.ItemCollection, uint, error) {
	var err error
	err = b.Open()
	if err != nil {
		return nil, 0, err
	}
	defer b.Close()
	return nil, 0, errors.NotImplementedf("BoltDB Load not implemented")
}

// LoadActivities
func (b *boltDB) LoadActivities(f s.Filterable) (as.ItemCollection, uint, error) {
	var err error
	err = b.Open()
	if err != nil {
		return nil, 0, err
	}
	defer b.Close()
	return loadFromBucket(b.d, b.root, []byte(bucketActivities), f)
}

// LoadObjects
func (b *boltDB) LoadObjects(f s.Filterable) (as.ItemCollection, uint, error) {
	var err error
	err = b.Open()
	if err != nil {
		return nil, 0, err
	}
	defer b.Close()
	return loadFromBucket(b.d, b.root, []byte(bucketObjects), f)
}

// LoadActors
func (b *boltDB) LoadActors(f s.Filterable) (as.ItemCollection, uint, error) {
	var err error
	err = b.Open()
	if err != nil {
		return nil, 0, err
	}
	defer b.Close()
	return loadFromBucket(b.d, b.root, []byte(bucketActors), f)
}

func descendInBucket(root *bolt.Bucket, path string) (*bolt.Bucket, string, error) {
	if root == nil {
		return nil, path, errors.Newf("Trying to descend into nil bucket")
	}
	if len(path) == 0 {
		return nil, path, errors.Newf("Trying to descend into nil bucket tree")
	}
	buckets := strings.Split(path, "/")

	lvl := 0
	b := root
	// descend the bucket tree up to the last found bucket
	for _, name := range buckets {
		lvl++
		if len(name) == 0 {
			continue
		}
		if b == nil {
			return root, path, errors.Errorf("trying to load from nil bucket")
		}
		cb := b.Bucket([]byte(name))
		if cb == nil {
			lvl--
			break
		}
		b = cb
	}
	path = strings.Join(buckets[lvl:], "/")

	return b, path, nil
}

// LoadCollection
func (b *boltDB) LoadCollection(f s.Filterable) (as.CollectionInterface, error) {
	var err error
	err = b.Open()
	if err != nil {
		return nil, err
	}
	defer b.Close()

	var ret as.CollectionInterface
	iri := f.ID()
	url, err := iri.URL()
	if err != nil {
		b.errFn("invalid IRI filter element %s when loading collections", iri)
	}
	if string(b.root) != url.Host {
		b.errFn("trying to load from non-local root bucket %s", url.Host)
	}
	col := &as.OrderedCollection{}
	col.ID = as.ObjectID(iri)
	col.Type = as.OrderedCollectionType

	err = b.d.View(func(tx *bolt.Tx) error {
		rb := tx.Bucket(b.root)
		if rb == nil {
			return errors.Newf("invalid root bucket %s", b.root)
		}
		bb, path, err := descendInBucket(rb, url.Path)
		if err != nil {
			b.errFn("unable to find %s in root bucket", path, b.root)
		}
		if len(path) == 0 {
			cb := bb.Cursor()
			if cb == nil {
				return errors.Errorf("Invalid collection bucket path %s", path)
			}
			for k, raw := cb.First(); k != nil; k, raw = cb.Next() {
				it, err := as.UnmarshalJSON(raw)
				if err != nil {
					return errors.Annotatef(err, "unable to unmarshal raw item")
				}
				if err = col.Append(it); err == nil {
					col.TotalItems++
				}
			}
		}
		return err
	})
	ret = col
	return ret, err
}

func save(db *bolt.DB, rootBkt []byte, it as.Item) (as.Item, error) {
	entryBytes, err := jsonld.Marshal(it)
	if err != nil {
		return it, errors.Annotatef(err, "could not marshal activity")
	}
	iri := it.GetLink()
	url, err := iri.URL()
	path := url.Path
	if err != nil {
		return it, errors.Annotatef(err,"invalid IRI")
	}

	var uuid string
	err = db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(rootBkt)
		if root == nil {
			return errors.Errorf("Invalid bucket %s", rootBkt)
		}
		if !root.Writable() {
			return errors.Errorf("Non writeable bucket %s", rootBkt)
		}
		var b *bolt.Bucket
		b, uuid, err = descendInBucket(root, path)
		if err != nil {
			return errors.Newf("Unable to find %s in root bucket", path)
		}
		if !b.Writable() {
			return errors.Errorf("Non writeable bucket %s", path)
		}
		err = b.Put([]byte(uuid), entryBytes)
		if err != nil {
			return fmt.Errorf("could not insert entry: %v", err)
		}

		return nil
	})

	return it, err
}

// SaveActivity
func (b *boltDB) SaveActivity(it as.Item) (as.Item, error) {
	var err error
	err = b.Open()
	if err != nil {
		return it, err
	}
	defer b.Close()
	iri := it.GetLink()
	if len(iri) == 0 {
		pc := as.IRI(fmt.Sprintf("%s/%s", b.baseURL, bucketActivities))
		if _, err = b.GenerateID(it, pc, nil); err != nil {
			return it, err
		}
	}
	if it, err = save(b.d, b.root, it); err == nil {
		b.logFn("Added new activity: %s", it.GetLink())
	}
	return it, err
}

// SaveObject
func (b *boltDB) SaveObject(it as.Item) (as.Item, error) {
	var err error
	err = b.Open()
	if err != nil {
		return it, err
	}
	defer b.Close()
	var bucket string
	if as.ActivityTypes.Contains(it.GetType()) {
		bucket = bucketActivities
	} else if as.ActorTypes.Contains(it.GetType()) {
		bucket = bucketActors
	} else {
		bucket = bucketObjects
	}
	iri := it.GetLink()
	if len(iri) == 0 {
		pc := as.IRI(fmt.Sprintf("%s/%s", b.baseURL, bucket))
		if _, err = b.GenerateID(it, pc, nil); err != nil {
			return it, err
		}
	}
	if it, err = save(b.d, b.root, it); err == nil {
		b.logFn("Added new %s: %s", bucket[:len(bucket)-1], it.GetLink())
	}
	return it, err
}

// UpdateObject
func (b *boltDB) UpdateObject(it as.Item) (as.Item, error) {
	var err error
	err = b.Open()
	if err != nil {
		return it, err
	}
	defer b.Close()
	return it, errors.NotImplementedf("UpdateObject not implemented in boltdb package")
}

// DeleteObject
func (b *boltDB) DeleteObject(it as.Item) (as.Item, error) {
	var err error
	err = b.Open()
	if err != nil {
		return it, err
	}
	defer b.Close()
	return it, errors.NotImplementedf("DeleteObject not implemented in boltdb package")
}

// GenerateID
func (b *boltDB) GenerateID(it as.Item, partOf as.IRI, by as.Item) (as.ObjectID, error) {
	uuid := uuid.New()
	id := as.ObjectID(fmt.Sprintf("%s/%s", strings.ToLower(string(partOf)), uuid))
	if as.ActivityTypes.Contains(it.GetType()) {
		a, err := ap.ToActivity(it)
		if err != nil {
			return id, err
		}
		a.ID = id
		it = a
	}
	if as.ActorTypes.Contains(it.GetType()) {
		p, err := ap.ToPerson(it)
		if err != nil {
			return id, err
		}
		p.ID = id
		it = p
	}
	if as.ObjectTypes.Contains(it.GetType()) {
		switch it.GetType() {
		case as.PlaceType:
			p, err := as.ToPlace(it)
			if err != nil {
				return id, err
			}
			p.ID = id
			it = p
		case as.ProfileType:
			p, err := as.ToProfile(it)
			if err != nil {
				return id, err
			}
			p.ID = id
			it = p
		case as.RelationshipType:
			p, err := as.ToRelationship(it)
			if err != nil {
				return id, err
			}
			p.ID = id
			it = p
		case as.TombstoneType:
			p, err := as.ToTombstone(it)
			if err != nil {
				return id, err
			}
			p.ID = id
			it = p
		default:
			p, err := as.ToObject(it)
			if err != nil {
				return id, err
			}
			p.ID = id
			it = p
		}
	}
	return id, nil
}

func (b *boltDB) Open() error {
	var err error
	b.d, err = bolt.Open(b.path, 0600, nil)
	if err != nil {
		return errors.Annotatef(err, "could not open db")
	}
	err = b.d.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(b.root)
		if root == nil {
			return errors.NotFoundf("root bucket %s not found", b.root)
		}
		if !root.Writable() {
			return errors.NotFoundf("root bucket %s is not writeable", b.root)
		}
		return nil
	})
	if err != nil {
		return errors.Annotatef(err, "db doesn't contain the correct bucket structure")
	}
	return nil
}

// Close closes the boltdb database if possible.
func (b *boltDB) Close() error {
	return b.d.Close()
}
