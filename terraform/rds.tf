# Creates a self-contained VPC rather than relying on the default VPC,
# which may have had its default subnets deleted.

data "aws_availability_zones" "available" {
  state = "available"
}

resource "aws_vpc" "pms" {
  cidr_block           = "10.0.0.0/16"
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags = { Name = "${var.project}-vpc" }
}

resource "aws_internet_gateway" "pms" {
  vpc_id = aws_vpc.pms.id
  tags   = { Name = "${var.project}-igw" }
}

# Two public subnets across two AZs — RDS subnet group requires >= 2 AZs
resource "aws_subnet" "pms" {
  count                   = 2
  vpc_id                  = aws_vpc.pms.id
  cidr_block              = cidrsubnet("10.0.0.0/16", 8, count.index)
  availability_zone       = data.aws_availability_zones.available.names[count.index]
  map_public_ip_on_launch = true
  tags = { Name = "${var.project}-subnet-${count.index}" }
}

resource "aws_route_table" "pms" {
  vpc_id = aws_vpc.pms.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.pms.id
  }
  tags = { Name = "${var.project}-rt" }
}

resource "aws_route_table_association" "pms" {
  count          = 2
  subnet_id      = aws_subnet.pms[count.index].id
  route_table_id = aws_route_table.pms.id
}

resource "aws_db_subnet_group" "pms" {
  name       = "${var.project}-db-subnet-group"
  subnet_ids = aws_subnet.pms[*].id
  tags       = { Name = "${var.project}-db-subnet-group" }
}

resource "aws_security_group" "rds" {
  name        = "${var.project}-rds-sg"
  description = "Allow PostgreSQL from anywhere (dev only - restrict in production)"
  vpc_id      = aws_vpc.pms.id

  ingress {
    from_port   = 5432
    to_port     = 5432
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }
}

resource "aws_db_instance" "pms" {
  identifier        = "${var.project}-postgres"
  engine            = "postgres"
  engine_version    = "15"
  instance_class    = "db.t3.micro"
  allocated_storage = 20
  storage_type      = "gp2"

  db_name  = var.db_name
  username = var.db_username
  password = var.db_password

  db_subnet_group_name   = aws_db_subnet_group.pms.name
  vpc_security_group_ids = [aws_security_group.rds.id]
  publicly_accessible    = true
  skip_final_snapshot    = true
  deletion_protection    = false

  tags = { Name = "${var.project}-postgres" }
}
