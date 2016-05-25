package test

import (
	"fmt"
	"os"
	"time"
	"treeless/server"
)

func gorStartCluster(numServers int) []testServer {
	l := make([]testServer, numServers)
	l[0] = new(gorServer)
	for i := 1; i < numServers; i++ {
		l[i] = new(gorServer)
	}
	return l
}

var gorID = 0

type gorServer struct {
	phy    string
	dbpath string
	server *server.DBServer
	closed bool
}

func (gs *gorServer) addr() string {
	return gs.phy
}

func (gs *gorServer) create(numChunks, redundancy int, verbose bool) string {
	gs.closed = false
	dbTestFolder := ""
	if exists("/mnt/dbs/") {
		dbTestFolder = "/mnt/dbs/"
	}
	gs.dbpath = dbTestFolder + "testDB" + fmt.Sprint(gorID)
	gs.server = server.Start("", "127.0.0.1", 10000+gorID, numChunks, redundancy, gs.dbpath, 1024*1024*128)
	gorID++
	gs.phy = string("127.0.0.1" + ":" + fmt.Sprint(10000+gorID-1))
	waitForServer(gs.phy)
	return gs.phy
}

func (gs *gorServer) assoc(addr string, verbose bool) string {
	gs.closed = false
	dbTestFolder := ""
	if exists("/mnt/dbs/") {
		dbTestFolder = "/mnt/dbs/"
	}
	gs.dbpath = dbTestFolder + "testDB" + fmt.Sprint(gorID)
	gs.server = server.Start(addr, "127.0.0.1", 10000+gorID, -1, -1, gs.dbpath, 1024*1024*128)
	gorID++
	gs.phy = string("127.0.0.1" + ":" + fmt.Sprint(10000+gorID-1))
	waitForServer(gs.phy)
	return gs.phy
}

func (gs *gorServer) kill() {
	if !gs.closed {
		gs.closed = true
		if gs.server != nil {
			gs.server.Stop()
			time.Sleep(time.Millisecond * 50)
			os.RemoveAll(gs.dbpath)
		}
	}
}

func (gs *gorServer) disconnect() {
	panic("Not implemented!")
}
func (gs *gorServer) reconnect() {
	panic("Not implemented!")
}
func (gs *gorServer) testCapability(c capability) bool {
	return c == capKill
}
