package main

import (
	"fmt"
	"strconv"
	"strings"
)

type PRRef struct {
	Owner  string
	Repo   string
	Number int
}

func parsePR(repoArg, numberArg string) (PRRef, error) {
	parts := strings.SplitN(repoArg, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return PRRef{}, fmt.Errorf("invalid repo format %q: expected owner/repo", repoArg)
	}

	num, err := strconv.Atoi(numberArg)
	if err != nil || num <= 0 {
		return PRRef{}, fmt.Errorf("invalid PR number %q: must be a positive integer", numberArg)
	}

	return PRRef{
		Owner:  parts[0],
		Repo:   parts[1],
		Number: num,
	}, nil
}
