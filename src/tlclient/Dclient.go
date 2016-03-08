package tlclient

import (
	"encoding/binary"
	"time"
	"treeless/src/tlcom"
	"treeless/src/tlhash"
	"treeless/src/tlheartbeat"
	"treeless/src/tlsg"
)

type DBClient struct {
	sg         *tlsg.ServerGroup
	GetTimeout time.Duration
	SetTimeout time.Duration
	DelTimeout time.Duration
	hbStop     func()
}

func Connect(addr string) (*DBClient, error) {
	c := new(DBClient)
	sg, err := tlsg.Assoc(addr)
	if err != nil {
		return nil, err
	}
	c.sg = sg

	//Start heartbeat listener
	c.hbStop = tlheartbeat.Start(sg, nil)

	c.GetTimeout = time.Millisecond * 500
	c.SetTimeout = time.Millisecond * 500
	c.DelTimeout = time.Millisecond * 500
	return c, nil
}

func (c *DBClient) Get(key []byte) (value []byte, lastTime time.Time) {
	//Last write wins policy
	chunkID := tlhash.GetChunkID(key, c.sg.NumChunks())
	servers := c.sg.GetChunkHolders(chunkID)
	var charray [8]chan tlcom.Result
	chs := 0

	for _, s := range servers {
		if s == nil {
			continue
		}
		ch := s.Get(key, c.GetTimeout)
		if ch == nil {
			continue
		}
		charray[chs] = ch
		chs++
	}
	for i := 0; i < chs; i++ {
		//log.Println("get rec")
		if charray[i] == nil {
			panic("charray[i]==nil")
		}
		r := <-charray[i]
		//log.Println("get rec compl")
		//log.Println("get rec unlock")
		if r.Err != nil {
			continue
		}
		v := r.Value
		if len(v) >= 8 {
			t := time.Unix(0, int64(binary.LittleEndian.Uint64(v[:8])))
			if lastTime.Before(t) {
				lastTime = t
				value = v
			}
		}
	}
	if value != nil {
		return value[8:], lastTime
	}
	return nil, lastTime
}

func (c *DBClient) Set(key, value []byte) (written bool, errs error) {
	chunkID := tlhash.GetChunkID(key, c.sg.NumChunks())
	servers := c.sg.GetChunkHolders(chunkID)
	valueWithTime := make([]byte, 8+len(value))
	binary.LittleEndian.PutUint64(valueWithTime, uint64(time.Now().UnixNano()))
	copy(valueWithTime[8:], value)
	for _, s := range servers {
		if s == nil {
			continue
		}
		err := s.Set(key, valueWithTime, c.SetTimeout)
		if err == nil {
			written = true
		} else {
			errs = err //TODO return only written
		}
	}
	return written, errs
}

func (c *DBClient) Del(key []byte) (errs error) {
	chunkID := tlhash.GetChunkID(key, c.sg.NumChunks())
	servers := c.sg.GetChunkHolders(chunkID)
	for _, s := range servers {
		if s == nil {
			continue
		}
		err := s.Del(key, c.DelTimeout)
		errs = err
	}
	return errs
}

func (c *DBClient) Close() {
	//Stop hearbeat
	c.hbStop()
	//Stop sockets
}