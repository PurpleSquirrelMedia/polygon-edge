package protocol

import (
	"fmt"
	"github.com/0xPolygon/polygon-edge/protocol/proto"
	"github.com/0xPolygon/polygon-edge/types"
	"github.com/libp2p/go-libp2p-core/peer"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"math/big"
	"sync"
	"time"
)

// Status defines the up to date information regarding the peer
type Status struct {
	Difficulty *big.Int   // Current difficulty
	Hash       types.Hash // Latest block hash
	Number     uint64     // Latest block number
}

// Copy creates a copy of the status
func (s *Status) Copy() *Status {
	ss := new(Status)
	ss.Hash = s.Hash
	ss.Number = s.Number
	ss.Difficulty = new(big.Int).Set(s.Difficulty)

	return ss
}

// toProto converts a Status object to a proto.V1Status
func (s *Status) toProto() *proto.V1Status {
	return &proto.V1Status{
		Number:     s.Number,
		Hash:       s.Hash.String(),
		Difficulty: s.Difficulty.String(),
	}
}

// fromProto converts a proto.V1Status to a Status object
func fromProto(status *proto.V1Status) (*Status, error) {
	diff, ok := new(big.Int).SetString(status.Difficulty, 10)
	if !ok {
		return nil, fmt.Errorf("failed to parse difficulty: %s", status.Difficulty)
	}

	return &Status{
		Number:     status.Number,
		Hash:       types.StringToHash(status.Hash),
		Difficulty: diff,
	}, nil
}

// statusFromProto extracts a Status object from a passed in proto.V1Status
func statusFromProto(p *proto.V1Status) (*Status, error) {
	s := new(Status)
	if err := s.Hash.UnmarshalText([]byte(p.Hash)); err != nil {
		return nil, err
	}

	s.Number = p.Number

	diff, ok := new(big.Int).SetString(p.Difficulty, 10)
	if !ok {
		return nil, fmt.Errorf("failed to decode difficulty")
	}

	s.Difficulty = diff

	return s, nil
}

// SyncPeer is a representation of the peer the node is syncing with
type SyncPeer struct {
	peer   peer.ID
	conn   *grpc.ClientConn
	client proto.V1Client

	status     *Status
	statusLock sync.RWMutex

	enqueueLock sync.Mutex
	enqueue     []*types.Block
	enqueueCh   chan struct{}
}

// Number returns the latest peer block height
func (s *SyncPeer) Number() uint64 {
	s.statusLock.RLock()
	defer s.statusLock.RUnlock()

	return s.status.Number
}

// IsClosed returns whether peer's connectivity has been closed
func (s *SyncPeer) IsClosed() bool {
	return s.conn.GetState() == connectivity.Shutdown
}

// purgeBlocks purges the cache of broadcasted blocks the node has written so far
// from the SyncPeer
func (s *SyncPeer) purgeBlocks(lastSeen types.Hash) {
	s.enqueueLock.Lock()
	defer s.enqueueLock.Unlock()

	indx := -1

	for i, b := range s.enqueue {
		if b.Hash() == lastSeen {
			indx = i
		}
	}

	if indx != -1 {
		s.enqueue = s.enqueue[indx+1:]
	}
}

// popBlock pops a block from the block queue [BLOCKING]
func (s *SyncPeer) popBlock(timeout time.Duration) (b *types.Block, err error) {
	timeoutCh := time.After(timeout)

	for {
		if !s.IsClosed() {
			s.enqueueLock.Lock()
			if len(s.enqueue) != 0 {
				b, s.enqueue = s.enqueue[0], s.enqueue[1:]
				s.enqueueLock.Unlock()

				return
			}

			s.enqueueLock.Unlock()
			select {
			case <-s.enqueueCh:
			case <-timeoutCh:
				return nil, ErrPopTimeout
			}
		} else {
			return nil, ErrConnectionClosed
		}
	}
}

// appendBlock adds a new block to the block queue
func (s *SyncPeer) appendBlock(b *types.Block) {
	s.enqueueLock.Lock()
	defer s.enqueueLock.Unlock()

	if len(s.enqueue) == maxEnqueueSize {
		// pop first element
		s.enqueue = s.enqueue[1:]
	}
	// append to the end
	s.enqueue = append(s.enqueue, b)

	select {
	case s.enqueueCh <- struct{}{}:
	default:
	}
}

func (s *SyncPeer) updateStatus(status *Status) {
	s.statusLock.Lock()
	defer s.statusLock.Unlock()

	s.status = status
}
