package pdc

import (
	"github.com/keroserene/go-webrtc"
	"bytes"
	"sync"
	"fmt"
	"errors"
)

func init() {
	webrtc.SetLoggingVerbosity(2)
}

var defaultConfig = webrtc.NewConfiguration()
//webrtc.OptionIceServer("stun:stun.l.google.com:19302"))

type Signaler interface {
	Send(*webrtc.SessionDescription) error
	Receive() (*webrtc.SessionDescription, error)
}

type PeerDataChannel struct {
	buf *bytes.Buffer
	pc *webrtc.PeerConnection
	dc *webrtc.DataChannel
	dcReady chan bool
	cond *sync.Cond
}

func (pdc *PeerDataChannel) dcOpen() {
	pdc.buf = new(bytes.Buffer)
	pdc.cond = &sync.Cond{L: &sync.Mutex{}}
	pdc.dcReady <- true
}

func (pdc *PeerDataChannel) dcClose() {
	// pdc.cond should NOT be nil, as it was already created in dcOpen
	pdc.cond.Signal()
}

func (pdc *PeerDataChannel) dcData(b []byte) {
	// at this point pdc.cond and pdc.buf should NOT be nil, as it was created in dcOpen,
	// and dcData should only be called after the DC has been opened
	pdc.cond.L.Lock()
	defer pdc.cond.L.Unlock()
	n, err := pdc.buf.Write(b)

	if n != len(b) || err != nil {
		panic(fmt.Sprintf("couldn't write to buffer, n=%d, len(b)=%d, err=%s", n, len(b), err))
	}

	pdc.cond.Signal()
}

func (pdc *PeerDataChannel) Read(b []byte) (n int, err error) {
	switch pdc.dc.ReadyState() {
	case webrtc.DataStateConnecting:
		return 0, errors.New("read: data channel is connecting")
	case webrtc.DataStateClosing:
		return 0, errors.New("read: data channel is closing")
	case webrtc.DataStateClosed:
		return 0, errors.New("read: data channel is closed")
	}

	pdc.cond.L.Lock()
	defer pdc.cond.L.Unlock()
	if pdc.buf.Len() == 0 {
		pdc.cond.Wait()
	}

	return pdc.buf.Read(b)
}

func (pdc *PeerDataChannel) Write(b []byte) (n int, err error) {
	switch pdc.dc.ReadyState() {
	case webrtc.DataStateConnecting:
		return 0, errors.New("write: data channel is connecting")
	case webrtc.DataStateClosing:
		return 0, errors.New("write: data channel is closing")
	case webrtc.DataStateClosed:
		return 0, errors.New("write: data channel is closed")
	}

	pdc.dc.Send(b)
	return len(b), nil
}

func (pdc *PeerDataChannel) Close() {
	pdc.dc.Close()
	pdc.pc.Destroy()
}

func NewPeerDataChannel(config *webrtc.Configuration, initiator bool, signaler Signaler) (*PeerDataChannel, error) {
	pc, err := webrtc.NewPeerConnection(config)
	if err != nil {
		return nil, err
	}

	pdc := &PeerDataChannel{
		//buf: new(bytes.Buffer), // moved to pdc.dcOpen()
		pc: pc,
		dcReady: make(chan bool),
	}

	// Used to wait on asynchronous errors and descriptions from event handlers
	errCh := make(chan error, 1)
	sdpCh := make(chan *webrtc.SessionDescription, 1)
	stateCh := make(chan webrtc.IceConnectionState)

	// Set up handlers

	pc.OnNegotiationNeeded = func() {
		// Create an offer
		go func() {
			offer, err := pc.CreateOffer()
			if err != nil {
				errCh <- err
				return
			}

			if err := pc.SetLocalDescription(offer); err != nil {
				errCh <- err
				return
			}
		}()
	}

	pc.OnIceComplete = func() {
		fmt.Println(initiator, ": ice complete")
		sdpCh <- pc.LocalDescription()
	}

	pc.OnDataChannel = func (dc *webrtc.DataChannel) {
		pdc.dc = dc

		// Set up data handlers
		pdc.dc.OnOpen = pdc.dcOpen
		pdc.dc.OnClose = pdc.dcClose
		pdc.dc.OnMessage = pdc.dcData
	}

	pc.OnIceConnectionStateChange = func(state webrtc.IceConnectionState) {
		go func() {
			fmt.Println(initiator, ": ice state:", state)
			stateCh <- state
		}()
	}

	// If we're the initiator try and create a channel
	if initiator {
		pdc.dc, err = pc.CreateDataChannel("channel")
		if err != nil {
			return nil, err
		}

		// Set up data handlers
		pdc.dc.OnOpen = pdc.dcOpen
		pdc.dc.OnClose = pdc.dcClose
		pdc.dc.OnMessage = pdc.dcData

		// Wait for ICE completion, or an error
		select {
		case err := <- errCh:
			return nil, err
		case offer := <- sdpCh:
			if err := signaler.Send(offer); err != nil {
				return nil, err
			}
		}

		// Wait for a response here
		answer, err := signaler.Receive()
		if err != nil {
			return nil, err
		}

		if err := pc.SetRemoteDescription(answer); err != nil {
			return nil, err
		}

	} else {
		// Otherwise, wait on an offer
		offer, err := signaler.Receive()
		if err != nil {
			return nil, err
		}

		if err := pc.SetRemoteDescription(offer); err != nil {
			return nil, err
		}

		// Create an answer
		go func() {
			answer, err := pc.CreateAnswer()
			if err != nil {
				errCh <- err
				return
			}

			if err := pc.SetLocalDescription(answer); err != nil {
				errCh <- err
				return
			}
		}()

		// Wait for ICE completion here, or an error
		select {
		case err := <- errCh:
			return nil, err
		case answer := <- sdpCh:
			if err := signaler.Send(answer); err != nil {
				return nil, err
			}
		}
	}

	// Wait on connection state
	outer:
	for {
		select {
		case iceState := <-stateCh:
			switch iceState {
			case webrtc.IceConnectionStateNew:
				continue
			case webrtc.IceConnectionStateChecking:
				continue
			case webrtc.IceConnectionStateConnected:
				break outer
			case webrtc.IceConnectionStateClosed:
				return nil, errors.New("closed")
			case webrtc.IceConnectionStateCompleted:
				return nil, errors.New("completed")
			case webrtc.IceConnectionStateDisconnected:
				return nil, errors.New("disconnected")
			case webrtc.IceConnectionStateFailed:
				return nil, errors.New("disconnected")
			}
		}
	}

	// Wait for channel to be ready
	<- pdc.dcReady
	return pdc, nil
}
