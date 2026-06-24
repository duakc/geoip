.PHONY: generate
generate:
	@go mod tidy
	@go run . --from "data" --output "data" --srs --mrs --all

.PHONY: testrun
testrun:
	@go mod tidy
	@go run . --from "./testdata" --output "./testoutput" --srs --mrs --all


.PHONY: fetch
fetch:
	@go mod tidy
	@go run script/fetch.go

.PHONY: fmt
fmt:
	go fmt .

.PHONY: clean
clean:
	rm -r data/
