IMAGE ?= egate:latest
CONTAINER ?= egate
PORT ?= 8119

.PHONY: init docker docker-build docker-stop docker-logs

init: docker-build data config
	@if [ ! -f config/.env ]; then \
		docker run --rm --user "$$(id -u):$$(id -g)" \
			-v "$(CURDIR)/config:/app/config" \
			$(IMAGE) --init-env /app/config/.env; \
		echo "Created config/.env. Replace the starter credentials and Resend API key before deployment."; \
	fi

data:
	mkdir -p data

config:
	mkdir -p config

docker-build:
	docker build -t $(IMAGE) .

docker: init
	@if grep -Eq '^(EGATE_ADMIN_PASSWORD=replace-with-a-long-random-password|EGATE_RESEND_API_KEY=invalid-replace-with-resend-api-key)$$' config/.env; then \
		echo "Edit config/.env to replace the starter credentials before starting egate."; \
		exit 1; \
	fi
	-docker rm -f $(CONTAINER) >/dev/null 2>&1
	docker run -d \
		--name $(CONTAINER) \
		--restart unless-stopped \
		--env-file "$(CURDIR)/config/.env" \
		-p $(PORT):$(PORT) \
		-v "$(CURDIR)/config:/app/config:ro" \
		-v "$(CURDIR)/data:/app/data" \
		$(IMAGE)

docker-stop:
	-docker rm -f $(CONTAINER)

docker-logs:
	docker logs -f $(CONTAINER)
