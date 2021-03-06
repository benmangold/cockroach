// Copyright 2017 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License.

package engine

import (
	"bytes"
	"encoding/binary"

	"github.com/cockroachdb/cockroach/pkg/roachpb"
	"github.com/cockroachdb/cockroach/pkg/storage/engine/enginepb"
	"github.com/golang/leveldb/db"
	"github.com/golang/leveldb/memfs"
	"github.com/golang/leveldb/table"
	"github.com/pkg/errors"
)

var readerOpts = &db.Options{
	Comparer: cockroachComparer{},
}

type sstIterator struct {
	sst  *table.Reader
	iter db.Iterator

	valid   bool
	err     error
	mvccKey MVCCKey

	// For allocation avoidance in NextKey.
	nextKeyStart []byte

	// fs is used to hold the in-memory filesystem for an in-memory reader. I
	// don't think there's a concrete reason that we need to hold a pointer to
	// it, but may as well.
	fs db.FileSystem

	// roachpb.Verify k/v pairs on each call to Next()
	verify bool
}

var _ SimpleIterator = &sstIterator{}

// NewSSTIterator returns a SimpleIterator for a leveldb formatted sstable on
// disk. It's compatible with sstables output by RocksDBSstFileWriter,
// which means the keys are CockroachDB mvcc keys and they each have the RocksDB
// trailer (of seqno & value type).
func NewSSTIterator(path string) (SimpleIterator, error) {
	file, err := db.DefaultFileSystem.Open(path)
	if err != nil {
		return nil, err
	}
	return &sstIterator{sst: table.NewReader(file, readerOpts)}, nil
}

// NewMemSSTIterator returns a SimpleIterator for a leveldb format sstable in
// memory. It's compatible with sstables output by RocksDBSstFileWriter,
// which means the keys are CockroachDB mvcc keys and they each have the RocksDB
// trailer (of seqno & value type).
func NewMemSSTIterator(data []byte, verify bool) (SimpleIterator, error) {
	fs := memfs.New()
	const filename = "data.sst"
	f, err := fs.Create(filename)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	file, err := fs.Open(filename)
	if err != nil {
		return nil, err
	}
	return &sstIterator{fs: fs, sst: table.NewReader(file, readerOpts), verify: verify}, nil
}

// Close implements the SimpleIterator interface.
func (r *sstIterator) Close() {
	if r.iter != nil {
		r.err = errors.Wrap(r.iter.Close(), "closing sstable iterator")
	}
	if err := r.sst.Close(); err != nil && r.err == nil {
		r.err = errors.Wrap(err, "closing sstable")
	}
}

// Seek implements the SimpleIterator interface.
func (r *sstIterator) Seek(key MVCCKey) {
	if r.iter != nil {
		if r.err = errors.Wrap(r.iter.Close(), "resetting sstable iterator"); r.err != nil {
			return
		}
	}
	r.iter = r.sst.Find(EncodeKey(key), nil)
	r.Next()
}

// Valid implements the SimpleIterator interface.
func (r *sstIterator) Valid() (bool, error) {
	return r.valid && r.err == nil, r.err
}

// Next implements the SimpleIterator interface.
func (r *sstIterator) Next() {
	if r.valid = r.iter.Next(); !r.valid {
		return
	}

	// RocksDB uses the last 8 bytes to pack the sequence number and value type
	// into a little-endian encoded uint64. The value type is stored in the
	// low byte and the sequence number is in the high 7 bytes. See dbformat.h.
	rocksdbInternalKey := r.iter.Key()
	if len(rocksdbInternalKey) < 8 {
		r.err = errors.Errorf("invalid rocksdb InternalKey: %x", rocksdbInternalKey)
		return
	}
	seqAndValueType := binary.LittleEndian.Uint64(rocksdbInternalKey[len(rocksdbInternalKey)-8:])
	if valueType := BatchType(seqAndValueType & 0xff); valueType != BatchTypeValue {
		r.err = errors.Errorf("value type not supported: %d", valueType)
		return
	}

	key := rocksdbInternalKey[:len(rocksdbInternalKey)-8]

	if k, ts, err := enginepb.DecodeKey(key); err == nil {
		r.mvccKey.Key = k
		r.mvccKey.Timestamp = ts
		r.err = nil
	} else {
		r.err = errors.Wrapf(err, "decoding key: %s", key)
		return
	}

	if r.verify {
		r.err = roachpb.Value{RawBytes: r.iter.Value()}.Verify(r.mvccKey.Key)
	}
}

// NextKey implements the SimpleIterator interface.
func (r *sstIterator) NextKey() {
	if !r.valid {
		return
	}
	r.nextKeyStart = append(r.nextKeyStart[:0], r.mvccKey.Key...)
	for r.Next(); r.valid && r.err == nil && bytes.Equal(r.nextKeyStart, r.mvccKey.Key); r.Next() {
	}
}

// UnsafeKey implements the SimpleIterator interface.
func (r *sstIterator) UnsafeKey() MVCCKey {
	return r.mvccKey
}

// UnsafeValue implements the SimpleIterator interface.
func (r *sstIterator) UnsafeValue() []byte {
	return r.iter.Value()
}

type cockroachComparer struct{}

var _ db.Comparer = cockroachComparer{}

// Compare implements the db.Comparer interface.
func (cockroachComparer) Compare(a, b []byte) int {
	keyA, tsA, okA := enginepb.SplitMVCCKey(a)
	keyB, tsB, okB := enginepb.SplitMVCCKey(b)
	if !okA || !okB {
		// This should never happen unless there is some sort of corruption of
		// the keys. This is a little bizarre, but the behavior exactly matches
		// engine/db.cc:DBComparator.
		return bytes.Compare(a, b)
	}

	if c := bytes.Compare(keyA, keyB); c != 0 {
		return c
	}
	if len(tsA) == 0 {
		if len(tsB) == 0 {
			return 0
		}
		return -1
	} else if len(tsB) == 0 {
		return 1
	}
	return bytes.Compare(tsB, tsA)
}

func (cockroachComparer) Name() string {
	// This must match the name in engine/db.cc:DBComparator::Name.
	return "cockroach_comparator"
}

func (cockroachComparer) AppendSeparator(dst, a, b []byte) []byte {
	panic("unimplemented")
}
