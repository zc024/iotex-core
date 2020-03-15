// Copyright (c) 2019 IoTeX Foundation
// This is an alpha (internal) release and is not suitable for production. This source code is provided 'as is' and no
// warranties are given as to title or non-infringement, merchantability or fitness for purpose and, to the extent
// permitted by law, all liability for your use of the code is disclaimed. This source code is governed by Apache
// License 2.0 that can be found in the LICENSE file.

package merklepatriciatree

import (
	"github.com/golang/protobuf/proto"
	"github.com/pkg/errors"

	"github.com/iotexproject/iotex-core/db/trie"
	"github.com/iotexproject/iotex-core/db/trie/triepb"
)

const radix = 256

type branchNode struct {
	cacheNode
	children map[byte]node
	isRoot   bool
}

func newBranchNode(
	mpt *merklePatriciaTree,
	children map[byte]node,
) (node, error) {
	if children == nil || len(children) == 0 {
		return nil, errors.New("branch node children cannot be empty")
	}
	bnode := &branchNode{cacheNode: cacheNode{mpt: mpt}, children: children}
	bnode.cacheNode.serializable = bnode
	if len(bnode.children) != 0 {
		return bnode.store()
	}
	return bnode, nil
}

func newEmptyRootBranchNode(mpt *merklePatriciaTree) *branchNode {
	bnode := &branchNode{cacheNode: cacheNode{mpt: mpt}, children: make(map[byte]node), isRoot: true}
	bnode.cacheNode.serializable = bnode

	return bnode
}

func newBranchNodeFromProtoPb(mpt *merklePatriciaTree, pb *triepb.BranchPb) *branchNode {
	bnode := &branchNode{cacheNode: cacheNode{mpt: mpt}, children: make(map[byte]node)}
	for _, n := range pb.Branches {
		bnode.children[byte(n.Index)] = newHashNode(mpt, n.Path)
	}
	bnode.cacheNode.serializable = bnode
	return bnode
}

func (b *branchNode) MarkAsRoot() {
	b.isRoot = true
}

func (b *branchNode) Children() []node {
	trieMtc.WithLabelValues("branchNode", "children").Inc()
	children := []node{}
	for _, child := range b.children {
		children = append(children, child)
	}
	return children
}

func (b *branchNode) Delete(key keyType, offset uint8) (node, error) {
	trieMtc.WithLabelValues("branchNode", "delete").Inc()
	offsetKey := key[offset]
	child, err := b.child(offsetKey)
	if err != nil {
		return nil, err
	}
	newChild, err := child.Delete(key, offset+1)
	if err != nil {
		return nil, err
	}
	if newChild != nil || b.isRoot {
		return b.updateChild(offsetKey, newChild, false)
	}
	switch len(b.children) {
	case 1:
		panic("branch shouldn't have 0 child after deleting")
	case 2:
		if err := b.delete(); err != nil {
			return nil, err
		}
		var orphan node
		var orphanKey byte
		for i, n := range b.children {
			if i != offsetKey {
				orphanKey = i
				orphan = n
				break
			}
		}
		if orphan == nil {
			panic("unexpected branch status")
		}
		if hn, ok := orphan.(*hashNode); ok {
			if orphan, err = hn.LoadNode(); err != nil {
				return nil, err
			}
		}
		switch node := orphan.(type) {
		case *extensionNode:
			return node.updatePath(
				append([]byte{orphanKey}, node.path...),
				false,
			)
		case *leafNode:
			return node, nil
		default:
			return newExtensionNode(b.mpt, []byte{orphanKey}, node)
		}
	default:
		return b.updateChild(offsetKey, newChild, false)
	}
}

func (b *branchNode) Upsert(key keyType, offset uint8, value []byte) (node, error) {
	trieMtc.WithLabelValues("branchNode", "upsert").Inc()
	var newChild node
	offsetKey := key[offset]
	child, err := b.child(offsetKey)
	switch errors.Cause(err) {
	case nil:
		newChild, err = child.Upsert(key, offset+1, value)
	case trie.ErrNotExist:
		newChild, err = newLeafNode(b.mpt, key, value)
	}
	if err != nil {
		return nil, err
	}

	return b.updateChild(offsetKey, newChild, true)
}

func (b *branchNode) Search(key keyType, offset uint8) (node, error) {
	trieMtc.WithLabelValues("branchNode", "search").Inc()
	child, err := b.child(key[offset])
	if err != nil {
		return nil, err
	}
	return child.Search(key, offset+1)
}

func (b *branchNode) proto() (proto.Message, error) {
	trieMtc.WithLabelValues("branchNode", "serialize").Inc()
	nodes := []*triepb.BranchNodePb{}
	for index := 0; index < radix; index++ {
		if c, ok := b.children[byte(index)]; ok {
			h, err := c.Hash()
			if err != nil {
				return nil, err
			}
			nodes = append(nodes, &triepb.BranchNodePb{Index: uint32(index), Path: h})
		}
	}

	return &triepb.NodePb{
		Node: &triepb.NodePb_Branch{
			Branch: &triepb.BranchPb{Branches: nodes},
		},
	}, nil
}

func (b *branchNode) child(key byte) (node, error) {
	c, ok := b.children[key]
	if !ok {
		return nil, trie.ErrNotExist
	}

	return c, nil
}

func (b *branchNode) updateChild(key byte, child node, h bool) (node, error) {
	if err := b.delete(); err != nil {
		return nil, err
	}
	if child == nil {
		delete(b.children, key)
	} else {
		b.children[key] = child
	}
	if len(b.children) != 0 {
		hn, err := b.store()
		if err != nil {
			return nil, err
		}
		if !b.isRoot && h {
			return hn, nil
		}
	} else {
		if err := b.calculateCache(); err != nil {
			return nil, err
		}
	}
	return b, nil
}