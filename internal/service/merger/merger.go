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
	TopSessions  []domain.SessionStat // top N across all hosts

	// HostsByProject[projectPath] -> list of (host, in, out) contributions,
	// sorted by tokens desc. Populated only when more than one host
	// contributes to the project.
	HostsByProject map[string][]HostContribution
}

type MachineInfo struct {
	Name     string
	TS       int64
	Total    int64 // tokens reported by this host in the window
	Projects int   // distinct projects on this host
	Sessions int   // sessions reported by this host
}

type HostContribution struct {
	Host   string
	In     int64
	Out    int64
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

	// Pick freshest by TS for rate-limit / subscription / window labels.
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
	// Working maps for per-host attribution.
	hostsByProject := map[string]map[string]*HostContribution{}

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

		for _, pj := range p.ByProject {
			key := pj.Path
			if key == "" {
				key = pj.Name
			}
			ex, ok := projects[key]
			if !ok {
				cp := pj
				projects[key] = &cp
			} else {
				ex.In += pj.In
				ex.Out += pj.Out
				ex.CacheR += pj.CacheR
				ex.CacheC += pj.CacheC
				ex.Sessions += pj.Sessions
			}

			// Record per-host contribution for this project.
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
		}

		for i := 0; i < 24 && i < len(p.Hourly24h); i++ {
			out.Hourly24h[i] += p.Hourly24h[i]
		}
		for i := 0; i < 7 && i < len(p.Daily7d); i++ {
			out.Daily7d[i] += p.Daily7d[i]
		}
		// Session IDs are unique per machine; union the lists then re-sort
		// and cap at the end.
		out.TopSessions = append(out.TopSessions, p.TopSessions...)
	}

	sort.Slice(out.TopSessions, func(i, j int) bool {
		return out.TopSessions[i].Total() > out.TopSessions[j].Total()
	})
	if len(out.TopSessions) > 10 {
		out.TopSessions = out.TopSessions[:10]
	}

	for _, v := range projects {
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

	sort.Slice(out.Machines, func(i, j int) bool { return out.Machines[i].Total > out.Machines[j].Total })

	// Flatten per-host attribution into sorted slices. Skip projects with
	// only one contributing host (no attribution to show).
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
