package main

import (
	"net/http"
	"time"

	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/weaveworks/scope/report"
)

const (
	websocketLoop    = 1 * time.Second
	websocketTimeout = 10 * time.Second
)

// APITopology is returned by the /api/topology/{name} handler.
type APITopology struct {
	Nodes map[string]report.RenderableNode `json:"nodes"`
}

// APINode is returned by the /api/topology/{name}/{id} handler.
type APINode struct {
	Node report.DetailedNode `json:"node"`
}

// APIEdge is returned by the /api/topology/*/*/* handlers.
type APIEdge struct {
	Metadata report.AggregateMetadata `json:"metadata"`
}

// topologySelecter selects a single topology from a report.
type topologySelecter func(r report.Report) report.Topology

func selectProcess(r report.Report) report.Topology {
	return r.Process
}

func selectNetwork(r report.Report) report.Topology {
	return r.Network
}

// Full topology.
func handleTopology(rep Reporter, t topologyView, w http.ResponseWriter, r *http.Request) {
	respondWith(w, http.StatusOK, APITopology{
		Nodes: t.selector(rep.Report()).RenderBy(t.mapper, t.pseudo),
	})
}

// Websocket for the full topology. This route overlaps with the next.
func handleWs(rep Reporter, t topologyView, w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		respondWith(w, http.StatusInternalServerError, err.Error())
		return
	}
	loop := websocketLoop
	if t := r.Form.Get("t"); t != "" {
		var err error
		if loop, err = time.ParseDuration(t); err != nil {
			respondWith(w, http.StatusBadRequest, t)
			return
		}
	}
	handleWebsocket(w, r, rep, t, loop)
}

// Individual nodes.
func handleNode(rep Reporter, t topologyView, w http.ResponseWriter, r *http.Request) {
	var (
		vars     = mux.Vars(r)
		nodeID   = vars["id"]
		rpt      = rep.Report()
		node, ok = t.selector(rpt).RenderBy(t.mapper, t.pseudo)[nodeID]
	)
	if !ok {
		http.NotFound(w, r)
		return
	}
	originHostFunc := func(id string) (OriginHost, bool) { return getOriginHost(rpt.HostMetadatas, id) }
	originNodeFunc := func(id string) (OriginNode, bool) { return getOriginNode(t.selector(rpt), id) }
	respondWith(w, http.StatusOK, APINode{Node: makeDetailed(node, originHostFunc, originNodeFunc)})
}

// Individual edges.
func handleEdge(rep Reporter, t topologyView, w http.ResponseWriter, r *http.Request) {
	var (
		vars     = mux.Vars(r)
		localID  = vars["local"]
		remoteID = vars["remote"]
		rpt      = rep.Report()
		metadata = t.selector(rpt).EdgeMetadata(t.mapper, localID, remoteID).Transform()
	)
	respondWith(w, http.StatusOK, APIEdge{Metadata: metadata})
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func handleWebsocket(
	w http.ResponseWriter,
	r *http.Request,
	rep Reporter,
	t topologyView,
	loop time.Duration,
) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// log.Println("Upgrade:", err)
		return
	}
	defer conn.Close()

	quit := make(chan struct{})
	go func(c *websocket.Conn) {
		for { // just discard everything the browser sends
			if _, _, err := c.NextReader(); err != nil {
				close(quit)
				break
			}
		}
	}(conn)

	var (
		previousTopo map[string]report.RenderableNode
		tick         = time.Tick(loop)
	)
	for {
		newTopo := t.selector(rep.Report()).RenderBy(t.mapper, t.pseudo)
		diff := report.TopoDiff(previousTopo, newTopo)
		previousTopo = newTopo

		if err := conn.SetWriteDeadline(time.Now().Add(websocketTimeout)); err != nil {
			return
		}
		if err := conn.WriteJSON(diff); err != nil {
			return
		}

		select {
		case <-quit:
			return
		case <-tick:
		}
	}
}
