package swarmdkg

import (
	"encoding/json"
	"fmt"
	"github.com/pkg/errors"
	"go.dedis.ch/kyber"
	"go.dedis.ch/kyber/pairing/bn256"
	rabin "go.dedis.ch/kyber/share/dkg/rabin"
	"go.dedis.ch/kyber/share/vss/rabin"

	"bytes"
	"encoding/gob"
	"go.dedis.ch/kyber/util/key"
	"sort"
	"time"
)

const (
	TIMEOUT_FOR_STATE = 10 * time.Second

	STATE_PUBKEY_SEND = iota
	STATE_PUBKEY_RECEIVE
	STATE_SEND_DEALS
	STATE_PROCESS_DEALS
	STATE_PROCESS_SEND_RESPONSES
	STATE_PROCESS_PROCESS_RESPONSES
)

var timeoutErr = errors.New("timeout")

type DKGMessage struct {
	From    int
	ToIndex int
	Data    []byte
}

type Streamer interface {
	Broadcast(msg []byte)
	Read() chan []byte
}

type DKG interface {
	SendPubkey() error
	ReceivePubkeys() error
	// Phase I
	SendDeals() error
	ProcessDeals() error
	ProcessResponses() error
	ProcessJustifications() error

	// Phase II
	ProcessCommits() error
	ProcessComplaints() error
	ProcessReconstructCommits() error
}

func NewDkg(streamer Streamer, suite *bn256.Suite, numOfNodes, threshold int) *DKGInstance {
	return &DKGInstance{
		Streamer:   streamer,
		Suite:      suite,
		NumOfNodes: numOfNodes,
		Treshold:   threshold,
		State:      STATE_PUBKEY_SEND,

		pubkeys: make([]kyber.Point, 0, numOfNodes),
	}
}

type DKGInstance struct {
	Streamer   Streamer
	NumOfNodes int
	Treshold   int
	Suite      *bn256.Suite
	State      int
	KeyPair    *key.Pair

	pubkeys  []kyber.Point
	Index    int
	dkgRabin *rabin.DistKeyGenerator
}

func (i *DKGInstance) SendPubkey() error {
	i.KeyPair = key.NewKeyPair(i.Suite)
	publicKeyBin, err := i.KeyPair.Public.MarshalBinary()
	if err != nil {
		return err
	}
	i.Streamer.Broadcast(publicKeyBin)
	return nil
}

func (i *DKGInstance) ReceivePubkeys() error {
	ch := i.Streamer.Read()
	for {
		select {
		case key := <-ch:
			point := i.Suite.Point()
			err := point.UnmarshalBinary(key)
			if err != nil {
				i.pubkeys = i.pubkeys[:0]
				return err
			}
			i.pubkeys = append(i.pubkeys, point)
		case <-time.After(TIMEOUT_FOR_STATE):
			i.pubkeys = i.pubkeys[:0]
			return timeoutErr
		}
		if len(i.pubkeys) == i.NumOfNodes {
			break
		}
	}
	return nil
}

func (i *DKGInstance) SendDeals() error {
	sort.Slice(i.pubkeys, func(k, m int) bool {
		return i.pubkeys[k].String() > i.pubkeys[m].String()
	})

	for j, p := range i.pubkeys {
		if p.Equal(i.KeyPair.Public) {
			i.Index = j
			break
		}
		if j == i.NumOfNodes-1 && i.Index == 0 {
			return errors.New("my key is not existed")
		}
	}

	var err error
	i.dkgRabin, err = rabin.NewDistKeyGenerator(i.Suite, i.KeyPair.Private, i.pubkeys, i.Treshold)
	if err != nil {
		return fmt.Errorf("Dkg instance init error: %v", err)
	}
	deals, err := i.dkgRabin.Deals()
	if err != nil {
		return fmt.Errorf("deal generation error: %v", err)
	}

	for toIndex, deal := range deals {
		b := bytes.NewBuffer(nil)
		err = gob.NewEncoder(b).Encode(deal)

		msg := DKGMessage{
			Data:    b.Bytes(),
			ToIndex: toIndex,
			From:    i.Index,
		}
		msgBin, err := json.Marshal(msg)
		if err != nil {
			return err
		}

		i.Streamer.Broadcast(msgBin)
	}
	return nil
}
func (i *DKGInstance) ProcessDeals() error {
	ch := i.Streamer.Read()
	numOfDeals := i.NumOfNodes - 1
	respList := make([]*rabin.Response, 0)

	for {
		select {
		case deal := <-ch:
			var msg DKGMessage
			err := json.Unmarshal(deal, &msg)
			if err != nil {
				return err
			}
			if msg.ToIndex != i.Index {
				continue
			}

			dd := &rabin.Deal{
				Deal: &vss.EncryptedDeal{
					DHKey: i.Suite.Point(),
				},
			}

			dec := gob.NewDecoder(bytes.NewBuffer(msg.Data))
			err = dec.Decode(dd)
			if err != nil {
				return err
			}

			resp, err := i.dkgRabin.ProcessDeal(dd)
			if err != nil {
				return err
			}
			respList = append(respList, resp)
			numOfDeals--

		case <-time.After(TIMEOUT_FOR_STATE):
			i.pubkeys = i.pubkeys[:0]
			return timeoutErr

		}
		if numOfDeals == 0 {
			break
		}
	}

	return nil
}
func (i *DKGInstance) ProcessResponses() error {
	return nil
}
func (i *DKGInstance) ProcessJustifications() error {
	return nil
}

// Phase II
func (i *DKGInstance) ProcessCommits() error {
	return nil
}
func (i *DKGInstance) ProcessComplaints() error {
	return nil
}
func (i *DKGInstance) ProcessReconstructCommits() error {
	return nil
}

func (i *DKGInstance) Run() error {
	for {
		switch i.State {
		case STATE_PUBKEY_SEND:
			err := i.SendPubkey()
			if err != nil {
				//todo errcheck
				i.moveToState(STATE_PUBKEY_SEND)
				panic(err)
			}
			i.moveToState(STATE_PUBKEY_RECEIVE)

		case STATE_PUBKEY_RECEIVE:
			err := i.ReceivePubkeys()
			if err != nil {
				//todo errcheck
				i.moveToState(STATE_PUBKEY_SEND)
				panic(err)
			}
			i.moveToState(STATE_SEND_DEALS)
		case STATE_SEND_DEALS:
			err := i.SendDeals()
			if err != nil {
				//todo errcheck
				i.moveToState(STATE_PUBKEY_SEND)
				panic(err)
			}
			i.moveToState(STATE_PROCESS_DEALS)
		case STATE_PROCESS_DEALS:
			err := i.ProcessDeals()
			if err != nil {
				//todo errcheck
				i.moveToState(STATE_PUBKEY_SEND)
				panic(err)
			}
			i.moveToState(STATE_PROCESS_SEND_RESPONSES)

		default:
			fmt.Println("default Exit")
			return errors.New("unknown state")
		}
	}
	return nil
}
func (i *DKGInstance) moveToState(state int) {
	fmt.Println("Move form", i.State, "to", state)
	i.State = state
}
