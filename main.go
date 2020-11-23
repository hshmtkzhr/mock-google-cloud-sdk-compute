package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

// initialize config and log instances which are used in the whole of this package
var config *ConfigMapper
var log *LogMapper
var startTime time.Time

func init() {
	var err error
	startTime = time.Now()
	//
	if config, err = NewConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "Initializing config instance failed: %v¥n", err)
		os.Exit(1)
	}
	//
	if log, err = NewLog(config); err != nil {
		fmt.Fprintf(os.Stderr, "Initializing log instance failed: %v¥n", err)
		os.Exit(1)
	}
}

func main() {
	if err := RootCmd.Execute(); err != nil {
		log.Fatal("Program aborted due to fatal error while initialzing command instance", err)
	}
}

// the following is the definition of subcommands provided by spf/cobra
func init() {
	RootCmd.AddCommand(ScrapeCmd)
	RootCmd.Flags().BoolVar(&rootVersion, "version", false, "print this version")

	ScrapeCmd.Flags().BoolVar(&scrapeExecFlag, "exec", false, "always required to perform scraping")
	ScrapeCmd.Flags().StringVar(&scrapeProjectID, "project", "", "gcp project id")
	ScrapeCmd.Flags().StringVar(&scrapeRegionName, "region", "", "region name")
	ScrapeCmd.Flags().StringVar(&scrapeGKEClusterName, "cluster-name", "", "target GKE cluster name")
}

var Version = "1.0"
var Revision = "00"

var rootVersion bool
var RootCmd = &cobra.Command{
	Use:   "mock-google-cloud-sdk-compute",
	Short: "sample of obtaining resources by sdk with paraller",
	Run: func(cmd *cobra.Command, args []string) {
		if rootVersion {
			fmt.Printf("version: %s-%s\n", Version, Revision)
			os.Exit(0)
		} else {
			_ = cmd.Help()
		}
	},
}

var scrapeExecFlag bool
var scrapeProjectID string
var scrapeRegionName string
var scrapeGKEClusterName string
var ScrapeCmd = &cobra.Command{
	Use:   "scrape",
	Short: "Scrape data for compute & gke cluster",
	Run: func(cmd *cobra.Command, args []string) {
		if scrapeExecFlag {
			var err error
			if err = setAndCheckMandatoryParams(config, scrapeProjectID, scrapeRegionName, scrapeGKEClusterName); err != nil {
				log.FatalWithError("abort due to parameter validation error: ", err)
			}
			ctx := context.Background()
			if err = performScraping(ctx, config); err != nil {
				log.FatalWithError("scraping ended with error: ", err)
			}
		} else {
			_ = cmd.Help()
		}
	},
}

func performScraping(ctx context.Context, config *ConfigMapper) error {
	uuid := uuid.New()
	log.WithFields(logrus.Fields{"status": "start", "id": uuid}).Info("Scraping Starts")

	objCompute := ComputeObject{}
	objCluster := ClusterObject{}

	var err error
	eg, ctx := errgroup.WithContext(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var stanza = func(status, processName string) {
		log.WithFields(logrus.Fields{"elapsed": timeTracker(), "status": status, "id": uuid}).Info(processName)
	}

	eg.Go(func() error {
		select {
		case <-ctx.Done():
			return nil
		default:
			stanza("processing", "obtain region and zones")
			objCompute, err = objCompute.New(ctx, config)
			if err != nil {
				return err
			}
			stanza("processing", "list instances in each zone")
			if err = objCompute.Instances.Get(&objCompute); err != nil {
				return err
			}
			return nil
		}
	})

	eg.Go(func() error {
		select {
		case <-ctx.Done():
			return nil
		default:
			stanza("processing", "obtain GKE cluster")
			objCluster, err := objCluster.New(ctx, config)
			if err != nil {
				return err
			}
			stanza("processing", "list instance-groups in the GKE cluster")
			err = objCluster.GetInstanceGroups()
			if err != nil {
				return err
			}

			return nil
		}
	})
	// wait and catch error
	if err := eg.Wait(); err != nil {
		cancel()
		return err
	}
	//
	stanza("processing", "list instances in each instance-group")
	err = objCluster.GetInstanceGroupNodes(&objCompute)
	if err != nil {
		return err
	}
	// end output
	log.WithFields(logrus.Fields{"total_elapsed_time": timeTracker(), "status": "end", "id": uuid}).Info("Scraping Complete")
	return nil
}

var setAndCheckMandatoryParams = func(config *ConfigMapper, p, r, g string) error {
	if p != "" {
		config.GCPConfig.ProjectID = p
	}
	if r != "" {
		config.GCPConfig.RegionName = r
	}
	if g != "" {
		config.GCPConfig.GKEClusterName = g
	}
	//
	if config.GCPConfig.ProjectID == "" {
		return errors.New("--project is empty. it should be specified as target gcp project name")
	}
	if config.GCPConfig.RegionName == "" {
		return errors.New("--region is empty. it should be specified as target region name")
	}
	if config.GCPConfig.GKEClusterName == "" {
		return errors.New("--cluster-name is empty. it should be specified as target gke cluster name")
	}
	return nil
}

var timeTracker = func() string {
	elapsed := time.Since(startTime)
	//return elapsed.Round(time.Millisecond).String()
	return elapsed.Round(time.Microsecond).String()
}
