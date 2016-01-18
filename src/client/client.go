package tlclient

import (
	"encoding/binary"
	"errors"
	"time"
	"treeless/src/com"
)

import "treeless/src/hash"

type Client struct {
	sg *tlcom.ServerGroup
}

func Connect(addr string) (*Client, error) {
	c := new(Client)
	sg, err := tlcom.ConnectAsClient(addr)
	if err != nil {
		return nil, err
	}
	c.sg = sg
	return c, nil
}

func (c *Client) Set(key, value []byte) error {
	chunkID := tlhash.GetChunkID(key, c.sg.NumChunks)
	holders := c.sg.GetChunkHolders(chunkID)
	conns := make([]*tlcom.ClientConn, 0, 4)
	var firstError error
	c.sg.Mutex.Lock()
	for _, h := range holders {
		err := h.NeedConnection()
		if err == nil {
			conns = append(conns, h.Conn)
		}
		if err != nil && firstError == nil {
			firstError = err
		}
	}
	c.sg.Mutex.Unlock()
	valueWithTime := make([]byte, 8+len(value))
	binary.LittleEndian.PutUint64(valueWithTime, uint64(time.Now().UnixNano()))
	copy(valueWithTime[8:], value)
	for _, c := range conns {
		err := c.Set(key, valueWithTime)
		if err != nil && firstError == nil {
			firstError = err
		}
	}
	return firstError
}

func (c *Client) Get(key []byte) ([]byte, time.Time, error) {
	chunkID := tlhash.GetChunkID(key, c.sg.NumChunks)
	holders := c.sg.GetChunkHolders(chunkID)
	var errs error = nil
	var value []byte
	//Last write wins policy
	lastTime := time.Unix(0, 0)
	for _, h := range holders {
		cerr := h.NeedConnection()
		if cerr != nil {
			if errs == nil {
				errs = cerr
			} else {
				errs = errors.New(errs.Error() + cerr.Error())
			}
			continue
		}
		v, err := h.Conn.Get(key)
		if err == nil {
			t := time.Unix(0, int64(binary.LittleEndian.Uint64(v[:8])))
			if lastTime.Before(t) {
				lastTime = t
				value = v
			}
		} else {
			if errs == nil {
				errs = err
			} else {
				errs = errors.New("Multiple errors: " + errs.Error() + ", " + err.Error())
			}
		}
	}
	if value != nil {
		return value[8:], lastTime, errs
	}
	return nil, time.Unix(0, 0), errs
}

func (c *Client) Close() {
	if c.sg != nil {
		c.sg.Stop()
	}
}