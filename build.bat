@echo off
path=%PATH%;c:\mingw64\bin
go install -ldflags -H=windowsgui .
