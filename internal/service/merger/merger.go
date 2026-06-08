// Package merger combines per-machine Status payloads into a single view.
// Rate-limit windows are account-scoped on Anthropic's side, so we keep the
// freshest report. Token aggregations are summed across machines, with
// per-machine attribution preserved alongside the merged totals.
package merger

import (
	"sort"

	"github.com/leonardorifeli/clawtop/internal/domain"
)

type Merged struct {
	Machines     []MachineInfo
	TS           int64
	Session      domain.Window
	Week         domain.Window
	Limit        string
	Subscription string
	Window       string
	Sessions     int
	ByProject    []domain.Project
	ByModel      []domain.Model
	Hourly24h    []int64
	Daily7d      []int64
	Heatmap      [168]int64
	WebSearch    int64
	WebFetch     int64
	Edits        int64
	Reads        int64
	Bash         int64
	TopSessions  []domain.SessionStat

	// HostsByProject[projectPath] -> list of (host, in, out) contributions,
	// sorted by tokens desc. Populated only when more than one host
	// contributes to the project.
	HostsByProject map[string][]HostContribution
}

type MachineInfo struct {
	Name     string
	TS       int64
	Total    int64
	Projects int
	Sessions int
}

type HostContribution struct {
	Host string
	In   int64
	Out  int64
}

func (c HostContribution) Total() int64 { return c.In + c.Out }

func Merge(parts []domain.Status) Merged {
	out := Merged{
		Hourly24h:      make([]int64, 24),
		Daily7d:        make([]int64, 7),
		HostsByProject: map[string][]HostContribution{},
	}
	if len(parts) == 0 {
		return out
	}

	freshest := 0
	for i := 1; i < len(parts); i++ {
		if parts[i].TS > parts[freshest].TS {
			freshest = i
		}
	}
	f := parts[freshest]
	out.TS = f.TS
	out.Session = f.Session
	out.Week = f.Week
	out.Limit = f.Limit
	out.Subscription = f.Subscription
	out.Window = f.Window

	projects := map[string]*domain.Project{}
	models := map[string]*domain.Model{}
	hostsByProject := map[string]map[string]*HostContribution{}
	// Branch aggregates merged across hosts, keyed by (path, branch).
	branches := map[string]map[string]*domain.BranchStat{}

	for _, p := range parts {
		var hostTotal int64
		for _, pj := range p.ByProject {
			hostTotal += pj.Total()
		}
		out.Machines = append(out.Machines, MachineInfo{
			Name:     p.Machine,
			TS:       p.TS,
			Total:    hostTotal,
			Projects: len(p.ByProject),
			Sessions: p.Sessions,
		})
		out.Sessions += p.Sessions
		out.WebSearch += p.WebSearch
		out.WebFetch += p.WebFetch
		out.Edits += p.Edits
		out.Reads += p.Reads
		out.Bash += p.Bash

		for _, pj := range p.ByProject {
			key := pj.Path
			if key == "" {
				key = pj.Name
			}
			ex, ok := projects[key]
			if !ok {
				cp := pj
				cp.Branches = nil // we'll rebuild branches below
				projects[key] = &cp
			} else {
				ex.In += pj.In
				ex.Out += pj.Out
				ex.CacheR += pj.CacheR
				ex.CacheC += pj.CacheC
				ex.Sessions += pj.Sessions
				ex.PrevIn += pj.PrevIn
				ex.PrevOut += pj.PrevOut
				if pj.LastAt > ex.LastAt {
					ex.LastAt = pj.LastAt
				}
			}

			// Branches: union across hosts.
			if len(pj.Branches) > 0 {
				if branches[key] == nil {
					branches[key] = map[string]*domain.BranchStat{}
				}
				for _, b := range pj.Branches {
					bb, ok := branches[key][b.Branch]
					if !ok {
						cp := b
						branches[key][b.Branch] = &cp
					} else {
						bb.In += b.In
						bb.Out += b.Out
						bb.Sessions += b.Sessions
					}
				}
			}

			if hostsByProject[key] == nil {
				hostsByProject[key] = map[string]*HostContribution{}
			}
			hc, ok := hostsByProject[key][p.Machine]
			if !ok {
				hc = &HostContribution{Host: p.Machine}
				hostsByProject[key][p.Machine] = hc
			}
			hc.In += pj.In
			hc.Out += pj.Out
		}

		for _, m := range p.ByModel {
			ex, ok := models[m.Model]
			if !ok {
				cp := m
				models[m.Model] = &cp
				continue
			}
			ex.In += m.In
			ex.Out += m.Out
			ex.CacheR += m.CacheR
			ex.CacheC += m.CacheC
			ex.Sessions += m.Sessions
			ex.PrevIn += m.PrevIn
			ex.PrevOut += m.PrevOut
			ex.WebSearch += m.WebSearch
			ex.WebFetch += m.WebFetch
		}

		for i := 0; i < 24 && i < len(p.Hourly24h); i++ {
			out.Hourly24h[i] += p.Hourly24h[i]
		}
		for i := 0; i < 7 && i < len(p.Daily7d); i++ {
			out.Daily7d[i] += p.Daily7d[i]
		}
		for i := 0; i < 168; i++ {
			out.Heatmap[i] += p.Heatmap[i]
		}
		out.TopSessions = append(out.TopSessions, p.TopSessions...)
	}

	sort.Slice(out.TopSessions, func(i, j int) bool {
		return out.TopSessions[i].Total() > out.TopSessions[j].Total()
	})
	if len(out.TopSessions) > 10 {
		out.TopSessions = out.TopSessions[:10]
	}

	// Attach merged branches to projects.
	for key, byBranch := range branches {
		if pj, ok := projects[key]; ok {
			list := make([]domain.BranchStat, 0, len(byBranch))
			for _, b := range byBranch {
				list = append(list, *b)
			}
			sort.Slice(list, func(i, j int) bool { return list[i].Total() > list[j].Total() })
			if len(list) > 8 {
				list = list[:8]
			}
			pj.Branches = list
		}
	}

	for _, v := range projects {
		if v.Total() == 0 {
			continue // skip projects with no token usage in this window
		}
		out.ByProject = append(out.ByProject, *v)
	}
	sort.Slice(out.ByProject, func(i, j int) bool {
		return out.ByProject[i].Total() > out.ByProject[j].Total()
	})

	for _, v := range models {
		out.ByModel = append(out.ByModel, *v)
	}
	sort.Slice(out.ByModel, func(i, j int) bool {
		return out.ByModel[i].Total() > out.ByModel[j].Total()
	})

	sort.Slice(out.Machines, func(i, j int) bool {
		return out.Machines[i].Total > out.Machines[j].Total
	})

	for key, byHost := range hostsByProject {
		if len(byHost) < 2 {
			continue
		}
		list := make([]HostContribution, 0, len(byHost))
		for _, hc := range byHost {
			list = append(list, *hc)
		}
		sort.Slice(list, func(i, j int) bool { return list[i].Total() > list[j].Total() })
		out.HostsByProject[key] = list
	}
	return out
}
