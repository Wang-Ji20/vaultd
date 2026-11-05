package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/crypto/bcrypt"

	"github.com/hortensia/vaultd/internal/config"
)

func hashPasswordCommand(args []string, stdin *os.File, stdout, stderr io.Writer) error {
	flags, where := newFlags("hash-password", stderr)
	write := flags.Bool("write", false, "save the hash in the config instead of printing it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	password, err := promptPassword(stdin, stderr)
	if err != nil {
		return err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	if !*write {
		fmt.Fprintln(stdout, hash)
		return nil
	}
	store, err := where.openStore()
	if err != nil {
		return err
	}
	if err := store.Update(func(c *config.Config) error {
		c.Admin.PasswordHash = hash
		return nil
	}); err != nil {
		return err
	}
	fmt.Fprintln(stderr, "Password saved in", store.Path())
	return nil
}

func hashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("password must not be empty")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if errors.Is(err, bcrypt.ErrPasswordTooLong) {
		return "", errors.New("password must be at most 72 bytes")
	}
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// promptPassword reads one line with echo off when stdin is a terminal. Piped
// input is read as it is, which makes scripted setups possible.
func promptPassword(stdin *os.File, stderr io.Writer) (string, error) {
	fmt.Fprint(stderr, "Admin password: ")
	restore := disableEcho(stdin)
	line, err := bufio.NewReader(stdin).ReadString('\n')
	restore()
	fmt.Fprintln(stderr)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
