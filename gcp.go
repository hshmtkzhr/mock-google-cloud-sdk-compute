package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sync"

	"github.com/pkg/errors"
	"golang.org/x/net/context"
	"golang.org/x/sync/errgroup"

	googlecompute "google.golang.org/api/compute/v1"
	googlecontainer "google.golang.org/api/container/v1"
)

var _ = ComputeObject{} // monkey patch to put linter's "unused" alerts silence

type ComputeObject struct {
	Lock      *sync.Mutex
	Service   *googlecompute.Service
	Project   *googlecompute.Project
	Region    *Region
	Instances *Instances
	Config    *ConfigMapper
}

func (obj ComputeObject) New(ctx context.Context, c *ConfigMapper) (ComputeObject, error) {
	var err error
	obj = ComputeObject{
		Lock:      &sync.Mutex{},
		Service:   &googlecompute.Service{},
		Project:   &googlecompute.Project{},
		Region:    &Region{},
		Instances: &Instances{},
		Config:    c,
	}

	if err = obj.SetService(ctx); err != nil {
		return obj, err
	}
	if err = obj.SetProject(); err != nil {
		return obj, err
	}
	if err = obj.SetRegion(); err != nil {
		return obj, err
	}

	return obj, nil
}

func (obj *ComputeObject) SetService(ctx context.Context) error {
	service, err := googlecompute.NewService(ctx)
	if err != nil {
		return errors.Wrapf(err, "unable to create googleapi service")
	}
	obj.Service = service
	return nil
}

func (obj *ComputeObject) SetProject() error {
	p, err := obj.Service.Projects.Get(obj.Config.GCPConfig.ProjectID).Do()
	if err != nil {
		return errors.Wrapf(err, "unable to get gcp project")
	}
	obj.Project = p
	return nil
}

func (obj *ComputeObject) SetRegion() error {
	r, err := obj.Service.Regions.Get(obj.Config.GCPConfig.ProjectID, obj.Config.GCPConfig.RegionName).Do()
	if err != nil {
		return errors.Wrapf(err, "unable to get region")
	}
	obj.Region = &Region{r, nil}
	obj.Region.SetZones()
	return nil
}

type Region struct {
	*googlecompute.Region
	ZoneList *Zones
}

func (region *Region) SetZones() {
	region.ZoneList = &Zones{}
	for _, z := range region.Zones {
		region.ZoneList.Push(z)
	}
}

type Zones []*Zone

func (zones *Zones) Push(zoneString string) {
	u, _ := url.Parse(zoneString)
	//if err != nil { // ideally, error should be handled. //}
	*zones = append(*zones, &Zone{
		Name:        path.Base(u.Path),
		OriginalURL: zoneString,
	})
}

type Zone struct {
	Name        string //asia-northeast1-a
	OriginalURL string //https://www.googleapis...../asia-northeast1-a
}

type Instances []*googlecompute.Instance

func (instances *Instances) Get(obj *ComputeObject) error {
	eg, ctx := errgroup.WithContext(context.Background())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var getInstanceInEachZone = func(pjID, zone string) error {
		req := obj.Service.Instances.List(pjID, zone)
		if err := req.Pages(ctx, func(Page *googlecompute.InstanceList) error {
			for _, instance := range Page.Items {
				obj.Lock.Lock()
				*instances = append(*instances, instance)
				obj.Lock.Unlock()
			}
			return nil
		}); err != nil {
			return errors.Wrapf(err, "unable to obtain compute instances")
		}
		return nil
	}

	for _, zone := range *obj.Region.ZoneList {
		zone := zone
		eg.Go(func() error {
			select {
			case <-ctx.Done():
				return errors.New(fmt.Sprintf("canceled: get instance in the zone(%s)", zone.Name))
			default:
				return getInstanceInEachZone(obj.Config.GCPConfig.ProjectID, zone.Name)
			}
		})
	}
	if err := eg.Wait(); err != nil {
		cancel()
		return err
	}
	return nil
}

var _ = ClusterObject{} // monkey patch to put linter's "unused" alerts silence

type ClusterObject struct {
	Lock           *sync.Mutex
	Service        *googlecontainer.Service
	ClusterObj     *googlecontainer.Cluster
	ClusterName    string
	InstanceGroups []*ClusterInstanceGroup
	Config         *ConfigMapper
}

type ClusterInstanceGroup struct {
	Project string
	Zone    string
	Name    string
	Nodes   []*ClusterNode
}

type ClusterNode struct {
	Name   string
	Status string
}

func (obj ClusterObject) New(ctx context.Context, c *ConfigMapper) (ClusterObject, error) {
	var err error
	obj = ClusterObject{}
	obj.Config = c
	obj.InstanceGroups = []*ClusterInstanceGroup{}
	obj.Lock = &sync.Mutex{}

	if err = obj.SetService(ctx); err != nil {
		return obj, err
	}
	if err = obj.Get(); err != nil {
		return obj, err
	}
	return obj, nil
}

func (obj *ClusterObject) SetService(ctx context.Context) error {
	service, err := googlecontainer.NewService(ctx)
	if err != nil {
		return errors.Wrapf(err, "unable to obtain google container service")
	}
	obj.Service = service
	return nil
}

func (obj *ClusterObject) Get() error {
	name := fmt.Sprintf(
		"projects/%s/locations/%s/clusters/%s",
		config.GCPConfig.ProjectID,
		config.GCPConfig.RegionName,
		config.GCPConfig.GKEClusterName,
	)
	c, err := obj.Service.Projects.Locations.Clusters.Get(name).Do()
	if err != nil {
		return errors.Wrapf(err, "unable to obtain gke cluster")
	}
	if c == nil {
		return errors.Wrapf(err, "no cluster with specified param: %s", name)
	}
	obj.ClusterObj = c
	obj.ClusterName = c.Name
	return nil
}

func (obj *ClusterObject) GetInstanceGroups() error {
	var reParseIGroupURL = regexp.MustCompile(`^.*/projects/([^\/]+)/zones/([^\/]+)/instanceGroupManagers/([^\/]+)`)
	for _, ig := range obj.ClusterObj.InstanceGroupUrls {
		match := reParseIGroupURL.FindStringSubmatch(ig)
		if len(match) != 4 {
			return errors.New("unexpected error, url didn't match correctly")
		}
		obj.InstanceGroups = append(obj.InstanceGroups,
			&ClusterInstanceGroup{Project: match[1], Zone: match[2], Name: match[3], Nodes: nil})
	}
	return nil
}

func (obj *ClusterObject) GetInstanceGroupNodes(objCompute *ComputeObject) error {
	var err error
	eg, ctx := errgroup.WithContext(context.Background())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	req := &googlecompute.InstanceGroupsListInstancesRequest{
		InstanceState: "ALL",
	}

	var getInstanceFromInstanceGroup = func(ig *ClusterInstanceGroup) error {
		reqIG := objCompute.Service.InstanceGroups.ListInstances(ig.Project, ig.Zone, ig.Name, req)
		err = reqIG.Pages(ctx, func(page *googlecompute.InstanceGroupsListInstances) error {
			for _, i := range page.Items {
				obj.Lock.Lock()
				ig.Nodes = append(ig.Nodes, &ClusterNode{Name: path.Base(i.Instance), Status: i.Status})
				obj.Lock.Unlock()
			}
			return nil
		})
		if err != nil {
			return errors.Wrapf(err, "InstanceGroups.ListInstances(%s) got error:", ig.Name)
		}
		return nil
	}

	for _, ig := range obj.InstanceGroups {
		ig := ig
		eg.Go(func() error {
			select {
			case <-ctx.Done():
				return errors.New(fmt.Sprintf("canceled: get instance in instace-group(%s)", ig.Name))
			default:
				return getInstanceFromInstanceGroup(ig)
			}
		})
	}
	if err := eg.Wait(); err != nil {
		cancel()
		return err
	}
	return nil
}

type Output struct {
	Code  int           `json:"code"`
	Nodes []*OutputNode `json:"nodes"`
	Error string        `json:"error"`
}

type OutputNode struct {
	Name               string `json:"name"`
	IPAddress          string `json:"ip"`
	ClusterBelongingTo string `json:"cluster"`
	Region             string `json:"region"`
	Zone               string `json:"zone"`
}

func (output Output) New() Output {
	return Output{
		Code:  200,
		Nodes: []*OutputNode{},
	}
}

func (output *Output) PushNode(node *OutputNode) {
	output.Nodes = append(output.Nodes, node)
}

func (output Output) PrintJSON() {
	b, _ := json.Marshal(output)
	fmt.Println(string(b))
}

func (output Output) Build(objCompute *ComputeObject, objCluster *ClusterObject) error {
	return nil
}
