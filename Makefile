.PHONY: generate
generate:
	@go mod tidy
	@go run . --from "data" --output "generated" --srs --mrs --mmdb --all

.PHONY: testrun
testrun:
	@go mod tidy
	@go run . --from "./testdata" --output "./testoutput" --srs --mrs --mmdb --all


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
