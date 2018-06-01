package pdc

import (
	"github.com/keroserene/go-webrtc"
	"sync"
	"fmt"
	"testing"
)

type channelSignaler struct {
	ch chan *webrtc.SessionDescription
}

func (c *channelSignaler) Send(desc *webrtc.SessionDescription) error {
	c.ch <- desc
	return nil
}

func (c *channelSignaler) Receive() (*webrtc.SessionDescription, error) {
	desc := <- c.ch
	return desc, nil
}

func newChannelSignaler() Signaler {
	return &channelSignaler{
		ch: make(chan *webrtc.SessionDescription),
	}
}

func TestNewPeerDataChannel(t *testing.T) {
	signaler := newChannelSignaler()
	wg := sync.WaitGroup{}

	sent := 0
	received := 0

	// Sender
	go func() {
		sender, err := NewPeerDataChannel(defaultConfig, true, signaler)
		if err != nil {
			fmt.Println("sender error:", err)
		}

		fmt.Println("sender ready!")

		for i := 0; i<1024; i++ {
			n, err := sender.Write([]byte(fmt.Sprintf("sending: %03d", i)))
			if err != nil {
				fmt.Println("sender error:", err)
				break
			}

			//fmt.Println("sender sent", n, "bytes")
			i++
			sent += n
		}

		sender.Close()
		fmt.Println("sender done!")
		wg.Done()
	}()

	// Receiver
	go func() {
		receiver, err := NewPeerDataChannel(defaultConfig, false, signaler)
		if err != nil {
			fmt.Println("receiver error:", err)
		}

		fmt.Println("receiver ready!")

		// close channel after a few seconds
		/*
		go func() {
			time.Sleep(3 * time.Second)
			receiver.Close()
		}()
		*/

		b := make([]byte, 32)
		for {
			n, err := receiver.Read(b)
			if err != nil {
				fmt.Println("receiver error:", err)
				break
			}

			//fmt.Printf("received %02d bytes: %s\n", n, b[:n])
			received += n
		}

		fmt.Println("receiver done!")
		wg.Done()
	}()

	wg.Add(2)
	wg.Wait()

	if sent != received {
		t.Fatal(fmt.Sprintf("sent=%d != received=%d", sent, received))
	}

	fmt.Printf("sent=%d, received=%d\n", sent, received)
}
