/*
Copyright 2022 The MultiCluster Traffic Controller Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package aws

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/route53"
	"github.com/go-logr/logr"
	"github.com/linki/instrumented_http"
	"github.com/pkg/errors"

	v1 "k8s.io/api/core/v1"
	kerrors "k8s.io/apimachinery/pkg/util/errors"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/Kuadrant/multicluster-gateway-controller/pkg/apis/v1alpha1"
	"github.com/Kuadrant/multicluster-gateway-controller/pkg/dns"
)

const (
	ProviderSpecificEvaluateTargetHealth       = "aws/evaluate-target-health"
	ProviderSpecificRegion                     = "aws/region"
	ProviderSpecificFailover                   = "aws/failover"
	ProviderSpecificGeolocationSubdivisionCode = "aws/geolocation-subdivision-code"
	ProviderSpecificMultiValueAnswer           = "aws/multi-value-answer"
	ProviderSpecificHealthCheckID              = "aws/health-check-id"
)

type Route53DNSProvider struct {
	client *route53.Route53
	logger logr.Logger
	// only consider hosted zones managing domains ending in this suffix
	//domainFilter endpoint.DomainFilter
	// filter hosted zones by id
	zoneIDFilter dns.ZoneIDFilter
	// filter hosted zones by type (e.g. private or public)
	//zoneTypeFilter provider.ZoneTypeFilter
	// filter hosted zones by tags
	//zoneTagFilter provider.ZoneTagFilter

	healthCheckReconciler dns.HealthCheckReconciler
}

var _ dns.Provider = &Route53DNSProvider{}

func NewProviderFromSecret(s *v1.Secret, providerConfig *v1alpha1.DNSProviderConfig) (*Route53DNSProvider, error) {
	if string(s.Data["AWS_ACCESS_KEY_ID"]) == "" || string(s.Data["AWS_SECRET_ACCESS_KEY"]) == "" {
		return nil, fmt.Errorf("AWS Provider credentials is empty")
	}

	config := aws.NewConfig().WithMaxRetries(3)
	config.WithHTTPClient(
		instrumented_http.NewClient(config.HTTPClient, &instrumented_http.Callbacks{
			PathProcessor: func(path string) string {
				parts := strings.Split(path, "/")
				return parts[len(parts)-1]
			},
		}),
	)
	config.WithCredentials(
		credentials.NewStaticCredentials(string(s.Data["AWS_ACCESS_KEY_ID"]), string(s.Data["AWS_SECRET_ACCESS_KEY"]), ""),
	)
	if string(s.Data["REGION"]) != "" {
		config.WithRegion(string(s.Data["REGION"]))
	}

	session, err := session.NewSessionWithOptions(session.Options{
		Config:            *config,
		SharedConfigState: session.SharedConfigDisable,
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to instantiate AWS session")
	}

	p := &Route53DNSProvider{
		client:       route53.New(session),
		logger:       log.Log.WithName("aws-route53").WithValues("region", config.Region),
		zoneIDFilter: dns.NewZoneIDFilter(providerConfig.ZoneIDFilter),
	}

	if err := validateServiceEndpoints(p); err != nil {
		return nil, fmt.Errorf("failed to validate AWS provider service endpoints: %v", err)
	}

	return p, nil
}

type action string

const (
	upsertAction action = "UPSERT"
	deleteAction action = "DELETE"
)

func (p *Route53DNSProvider) ListZones() (dns.ZoneList, error) {
	var zoneList dns.ZoneList
	zones, err := p.zones()
	if err != nil {
		return zoneList, err
	}
	for _, zone := range zones {
		dnsName := removeTrailingDot(*zone.Name)
		zoneList.Items = append(zoneList.Items, &dns.Zone{
			ID:   zone.Id,
			Name: &dnsName,
		})
	}
	return zoneList, nil
}

func (p *Route53DNSProvider) Ensure(record *v1alpha1.DNSRecord) error {
	return p.change(record, upsertAction)
}

func (p *Route53DNSProvider) Delete(record *v1alpha1.DNSRecord) error {
	return p.change(record, deleteAction)
}

func (p *Route53DNSProvider) HealthCheckReconciler() dns.HealthCheckReconciler {
	if p.healthCheckReconciler == nil {
		p.healthCheckReconciler = dns.NewCachedHealthCheckReconciler(
			p,
			NewRoute53HealthCheckReconciler(*p.client),
		)
	}

	return p.healthCheckReconciler
}

func (*Route53DNSProvider) ProviderSpecific() dns.ProviderSpecificLabels {
	return dns.ProviderSpecificLabels{
		Weight:        dns.ProviderSpecificWeight,
		HealthCheckID: ProviderSpecificHealthCheckID,
	}
}

// Zones returns the list of hosted zones.
func (p *Route53DNSProvider) zones() (map[string]*route53.HostedZone, error) {
	//if p.zonesCache.zones != nil && time.Since(p.zonesCache.age) < p.zonesCache.duration {
	//	log.Debug("Using cached zones list")
	//	return p.zonesCache.zones, nil
	//}
	//log.Debug("Refreshing zones list cache")

	zones := make(map[string]*route53.HostedZone)

	var tagErr error
	f := func(resp *route53.ListHostedZonesOutput, lastPage bool) (shouldContinue bool) {
		for _, zone := range resp.HostedZones {
			if !p.zoneIDFilter.Match(aws.StringValue(zone.Id)) {
				continue
			}
			//
			//if !p.zoneTypeFilter.Match(zone) {
			//	continue
			//}
			//
			//if !p.domainFilter.Match(aws.StringValue(zone.Name)) {
			//	continue
			//}

			// Only fetch tags if a tag filter was specified
			//if !p.zoneTagFilter.IsEmpty() {
			//	tags, err := p.tagsForZone(ctx, *zone.Id)
			//	if err != nil {
			//		tagErr = err
			//		return false
			//	}
			//	if !p.zoneTagFilter.Match(tags) {
			//		continue
			//	}
			//}

			zones[aws.StringValue(zone.Id)] = zone
		}

		return true
	}

	err := p.client.ListHostedZonesPages(&route53.ListHostedZonesInput{}, f)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list hosted zones")
	}
	if tagErr != nil {
		return nil, errors.Wrap(tagErr, "failed to list zones tags")
	}

	for _, zone := range zones {
		log.Log.V(1).Info("Considering zone", "zone.Id", aws.StringValue(zone.Id), "zone.Name", aws.StringValue(zone.Name))
	}

	//if p.zonesCache.duration > time.Duration(0) {
	//	p.zonesCache.zones = zones
	//	p.zonesCache.age = time.Now()
	//}

	return zones, nil
}

func (p *Route53DNSProvider) change(record *v1alpha1.DNSRecord, action action) error {
	// Configure records.
	if len(record.Spec.Endpoints) == 0 {
		return nil
	}
	err := p.updateRecord(record, string(action))
	if err != nil {
		return fmt.Errorf("failed to update record in route53 hosted zone %s: %v", *record.Spec.ZoneID, err)
	}
	switch action {
	case upsertAction:
		p.logger.Info("Upserted DNS record", "record", record.Spec, "hostedZoneID", *record.Spec.ZoneID)
	case deleteAction:
		p.logger.Info("Deleted DNS record", "record", record.Spec, "hostedZoneID", *record.Spec.ZoneID)
	}
	return nil
}

func (p *Route53DNSProvider) updateRecord(record *v1alpha1.DNSRecord, action string) error {

	if len(record.Spec.Endpoints) == 0 {
		return fmt.Errorf("no endpoints")
	}

	input := route53.ChangeResourceRecordSetsInput{HostedZoneId: aws.String(*record.Spec.ZoneID)}

	expectedEndpointsMap := make(map[string]struct{})
	var changes []*route53.Change
	for _, endpoint := range record.Spec.Endpoints {
		expectedEndpointsMap[endpoint.SetID()] = struct{}{}
		change, err := p.changeForEndpoint(endpoint, action)
		if err != nil {
			return err
		}
		changes = append(changes, change)
	}

	// Delete any previously published records that are no longer present in record.Spec.Endpoints
	if action != string(deleteAction) {
		lastPublishedEndpoints := record.Status.Endpoints
		for _, endpoint := range lastPublishedEndpoints {
			if _, found := expectedEndpointsMap[endpoint.SetID()]; !found {
				change, err := p.changeForEndpoint(endpoint, string(deleteAction))
				if err != nil {
					return err
				}
				changes = append(changes, change)
			}
		}
	}

	if len(changes) == 0 {
		return nil
	}
	input.ChangeBatch = &route53.ChangeBatch{
		Changes: changes,
	}
	resp, err := p.client.ChangeResourceRecordSets(&input)
	if err != nil {
		return fmt.Errorf("couldn't update DNS record %s in zone %s: %v", record.Name, *record.Spec.ZoneID, err)
	}
	p.logger.Info("Updated DNS record", "record", record, "zone", *record.Spec.ZoneID, "response", resp)
	return nil
}

func (p *Route53DNSProvider) changeForEndpoint(endpoint *v1alpha1.Endpoint, action string) (*route53.Change, error) {
	if endpoint.RecordType != string(v1alpha1.ARecordType) && endpoint.RecordType != string(v1alpha1.CNAMERecordType) && endpoint.RecordType != string(v1alpha1.NSRecordType) {
		return nil, fmt.Errorf("unsupported record type %s", endpoint.RecordType)
	}
	domain, targets := endpoint.DNSName, endpoint.Targets
	if len(domain) == 0 {
		return nil, fmt.Errorf("domain is required")
	}
	if len(targets) == 0 {
		return nil, fmt.Errorf("targets is required")
	}

	var resourceRecords []*route53.ResourceRecord
	for _, target := range endpoint.Targets {
		resourceRecords = append(resourceRecords, &route53.ResourceRecord{Value: aws.String(target)})
	}

	resourceRecordSet := &route53.ResourceRecordSet{
		Name:            aws.String(endpoint.DNSName),
		Type:            aws.String(endpoint.RecordType),
		TTL:             aws.Int64(int64(endpoint.RecordTTL)),
		ResourceRecords: resourceRecords,
	}

	if endpoint.SetIdentifier != "" {
		resourceRecordSet.SetIdentifier = aws.String(endpoint.SetIdentifier)
	}
	if prop, ok := endpoint.GetProviderSpecificProperty(dns.ProviderSpecificWeight); ok {
		weight, err := strconv.ParseInt(prop.Value, 10, 64)
		if err != nil {
			p.logger.Error(err, "Failed parsing value, using weight of 0", "weight", dns.ProviderSpecificWeight, "value", prop.Value)
			weight = 0
		}
		resourceRecordSet.Weight = aws.Int64(weight)
	}
	if prop, ok := endpoint.GetProviderSpecificProperty(ProviderSpecificRegion); ok {
		resourceRecordSet.Region = aws.String(prop.Value)
	}
	if prop, ok := endpoint.GetProviderSpecificProperty(ProviderSpecificFailover); ok {
		resourceRecordSet.Failover = aws.String(prop.Value)
	}
	if _, ok := endpoint.GetProviderSpecificProperty(ProviderSpecificMultiValueAnswer); ok {
		resourceRecordSet.MultiValueAnswer = aws.Bool(true)
	}

	var geolocation = &route53.GeoLocation{}
	useGeolocation := false

	if prop, ok := endpoint.GetProviderSpecificProperty(dns.ProviderSpecificGeoCode); ok {
		if dns.IsISO3166Alpha2Code(prop.Value) || dns.GeoCode(prop.Value).IsWildcard() {
			geolocation.CountryCode = aws.String(prop.Value)
		} else {
			geolocation.ContinentCode = aws.String(prop.Value)
		}
		useGeolocation = true
	}

	if geolocation.ContinentCode == nil {
		if prop, ok := endpoint.GetProviderSpecificProperty(ProviderSpecificGeolocationSubdivisionCode); ok {
			geolocation.SubdivisionCode = aws.String(prop.Value)
			useGeolocation = true
		}
	}
	if useGeolocation {
		resourceRecordSet.GeoLocation = geolocation
	}

	if prop, ok := endpoint.GetProviderSpecificProperty(ProviderSpecificHealthCheckID); ok {
		resourceRecordSet.HealthCheckId = aws.String(prop.Value)
	}

	change := &route53.Change{
		Action:            aws.String(action),
		ResourceRecordSet: resourceRecordSet,
	}
	return change, nil
}

// validateServiceEndpoints validates that provider clients can communicate with
// associated API endpoints by having each client make a list/describe/get call.
func validateServiceEndpoints(provider *Route53DNSProvider) error {
	var errs []error
	zoneInput := route53.ListHostedZonesInput{MaxItems: aws.String("1")}
	if _, err := provider.client.ListHostedZones(&zoneInput); err != nil {
		errs = append(errs, fmt.Errorf("failed to list route53 hosted zones: %v", err))
	}
	return kerrors.NewAggregate(errs)
}

// removeTrailingDot ensures that the hostname receives a trailing dot if it hasn't already.
func removeTrailingDot(hostname string) string {
	if net.ParseIP(hostname) != nil {
		return hostname
	}

	return strings.TrimSuffix(hostname, ".")
}
