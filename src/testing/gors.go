package tltest

import (
	"fmt"
	"os"
	"time"
	"treeless/src/tlserver"
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
	server *tlserver.DBServer
}

func (gs *gorServer) addr() string {
	return gs.phy
}

func (gs *gorServer) create(numChunks, redundancy int) string {
	dbTestFolder := ""
	if exists("/mnt/dbs/") {
		dbTestFolder = "/mnt/dbs/"
	}
	gs.dbpath = dbTestFolder + "testDB" + fmt.Sprint(gorID)
	gs.server = tlserver.Start("", "127.0.0.1", 10000+gorID, numChunks, redundancy, gs.dbpath)
	gorID++
	gs.phy = string("127.0.0.1" + ":" + fmt.Sprint(10000+gorID-1))
	waitForServer(gs.phy)
	return gs.phy
}

func (gs *gorServer) assoc(addr string) string {
	dbTestFolder := ""
	if exists("/mnt/dbs/") {
		dbTestFolder = "/mnt/dbs/"
	}
	gs.dbpath = dbTestFolder + "testDB" + fmt.Sprint(gorID)
	gs.server = tlserver.Start(addr, "127.0.0.1", 10000+gorID, -1, -1, gs.dbpath)
	gorID++
	gs.phy = string("127.0.0.1" + ":" + fmt.Sprint(10000+gorID-1))
	waitForServer(gs.phy)
	return gs.phy
}

func (gs *gorServer) kill() {
	gs.server.Stop()
	time.Sleep(time.Millisecond * 10)
	os.RemoveAll(gs.dbpath)
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