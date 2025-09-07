package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type Config struct {
	concurrency  int
	timeout      time.Duration
	verbose      bool
	ipv6         bool
	showIPs      bool
	resolvers    []string
	retries      int
}

var config Config

func init() {
	flag.IntVar(&config.concurrency, "c", 20, "Number of concurrent workers")
	flag.DurationVar(&config.timeout, "t", 5*time.Second, "DNS resolution timeout")
	flag.BoolVar(&config.verbose, "v", false, "Verbose output (show errors)")
	flag.BoolVar(&config.ipv6, "6", false, "Also check IPv6 resolution")
	flag.BoolVar(&config.showIPs, "show-ips", false, "Show resolved IP addresses")
	flag.IntVar(&config.retries, "r", 2, "Number of retries for failed resolutions")
}

func main() {
	flag.Parse()

	// Use system resolvers by default
	config.resolvers = []string{""}

	jobs := make(chan string, config.concurrency*2)
	results := make(chan string, config.concurrency)

	ctx := context.Background()
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < config.concurrency; i++ {
		wg.Add(1)
		go worker(ctx, jobs, results, &wg)
	}

	// Start result printer
	go func() {
		for result := range results {
			fmt.Println(result)
		}
	}()

	// Read domains from stdin
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		domain := strings.TrimSpace(scanner.Text())
		if domain == "" {
			continue
		}
		
		// Clean domain (remove protocol if present)
		domain = cleanDomain(domain)
		if domain != "" {
			jobs <- domain
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "Error reading input: %v\n", err)
	}

	close(jobs)
	wg.Wait()
	close(results)
}

func worker(ctx context.Context, jobs <-chan string, results chan<- string, wg *sync.WaitGroup) {
	defer wg.Done()

	resolver := &net.Resolver{
		PreferGo: true,
		StrictErrors: false,
	}

	for domain := range jobs {
		if resolves(ctx, resolver, domain) {
			if config.showIPs {
				ips := getIPs(ctx, resolver, domain)
				if len(ips) > 0 {
					results <- fmt.Sprintf("%s [%s]", domain, strings.Join(ips, ", "))
				} else {
					results <- domain
				}
			} else {
				results <- domain
			}
		} else if config.verbose {
			fmt.Fprintf(os.Stderr, "Does not resolve: %s\n", domain)
		}
	}
}

func resolves(ctx context.Context, resolver *net.Resolver, domain string) bool {
	// Try with retries
	for attempt := 0; attempt <= config.retries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Millisecond * 100 * time.Duration(attempt))
		}

		ctx, cancel := context.WithTimeout(ctx, config.timeout)
		defer cancel()

		// Try IPv4 resolution
		addrs, err := resolver.LookupIPAddr(ctx, domain)
		if err == nil && len(addrs) > 0 {
			// Check if we got actual IPs
			hasIPv4 := false
			hasIPv6 := false
			
			for _, addr := range addrs {
				if addr.IP.To4() != nil {
					hasIPv4 = true
				} else if addr.IP.To16() != nil {
					hasIPv6 = true
				}
			}
			
			// Return true if we have IPv4, or IPv6 if requested
			if hasIPv4 || (config.ipv6 && hasIPv6) {
				return true
			}
		}

		// Try CNAME resolution as fallback
		cname, err := resolver.LookupCNAME(ctx, domain)
		if err == nil && cname != "" && cname != domain && cname != domain+"." {
			// Domain has a CNAME, try to resolve it
			if cname != domain+"." {
				return resolves(ctx, resolver, strings.TrimSuffix(cname, "."))
			}
		}
	}

	return false
}

func getIPs(ctx context.Context, resolver *net.Resolver, domain string) []string {
	ctx, cancel := context.WithTimeout(ctx, config.timeout)
	defer cancel()

	var ips []string
	addrs, err := resolver.LookupIPAddr(ctx, domain)
	if err != nil {
		return ips
	}

	seen := make(map[string]bool)
	for _, addr := range addrs {
		ip := addr.IP.String()
		if !seen[ip] {
			seen[ip] = true
			// Filter IPv6 if not requested
			if !config.ipv6 && addr.IP.To4() == nil {
				continue
			}
			ips = append(ips, ip)
		}
	}

	return ips
}

func cleanDomain(domain string) string {
	// Remove common URL prefixes
	domain = strings.TrimPrefix(domain, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "ftp://")
	domain = strings.TrimPrefix(domain, "ws://")
	domain = strings.TrimPrefix(domain, "wss://")
	
	// Remove path if present
	if idx := strings.Index(domain, "/"); idx != -1 {
		domain = domain[:idx]
	}
	
	// Remove port if present
	if idx := strings.LastIndex(domain, ":"); idx != -1 {
		// Check if it's not IPv6
		if !strings.Contains(domain, "[") {
			domain = domain[:idx]
		}
	}
	
	// Clean whitespace
	domain = strings.TrimSpace(domain)
	
	// Validate basic domain format
	if domain == "" || strings.Contains(domain, " ") {
		return ""
	}
	
	return domain
}