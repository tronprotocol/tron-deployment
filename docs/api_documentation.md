# API Documentation

Welcome to the API Documentation for the repository. This guide provides detailed information on the available endpoints and their usage.

## Table of Contents

1. [Introduction](#introduction)
2. [Endpoints](#endpoints)
3. [Request and Response Formats](#request-and-response-formats)
4. [Authentication](#authentication)
5. [Error Handling](#error-handling)
6. [Examples](#examples)
7. [Additional Resources](#additional-resources)

## Introduction

This repository provides APIs to interact with the TRON network. The APIs allow you to perform various operations, such as querying account information, sending transactions, and interacting with smart contracts.

## Endpoints

### Get Account Information

**Endpoint**: `/wallet/getaccount`

**Method**: POST

**Description**: Retrieves account information for a given address.

**Request Parameters**:

- `address` (string): The address of the account.

**Response**:

```json
{
  "address": "string",
  "balance": "number",
  "create_time": "number",
  "latest_opration_time": "number",
  "latest_consume_free_time": "number",
  "account_resource": {
    "energy_usage": "number",
    "energy_limit": "number",
    "net_usage": "number",
    "net_limit": "number"
  }
}
```

### Send Transaction

**Endpoint**: `/wallet/createtransaction`

**Method**: POST

**Description**: Creates a transaction to transfer TRX from one account to another.

**Request Parameters**:

- `owner_address` (string): The address of the sender.
- `to_address` (string): The address of the recipient.
- `amount` (number): The amount of TRX to transfer.

**Response**:

```json
{
  "txID": "string",
  "raw_data": {
    "contract": [
      {
        "parameter": {
          "value": {
            "amount": "number",
            "owner_address": "string",
            "to_address": "string"
          },
          "type_url": "string"
        },
        "type": "string"
      }
    ],
    "ref_block_bytes": "string",
    "ref_block_hash": "string",
    "expiration": "number",
    "timestamp": "number"
  },
  "raw_data_hex": "string"
}
```

### Get Transaction Information

**Endpoint**: `/wallet/gettransactionbyid`

**Method**: POST

**Description**: Retrieves information about a transaction by its ID.

**Request Parameters**:

- `value` (string): The transaction ID.

**Response**:

```json
{
  "id": "string",
  "fee": "number",
  "blockNumber": "number",
  "blockTimeStamp": "number",
  "contractResult": ["string"],
  "receipt": {
    "net_usage": "number",
    "energy_usage": "number",
    "result": "string"
  },
  "log": [
    {
      "address": "string",
      "topics": ["string"],
      "data": "string"
    }
  ]
}
```

## Request and Response Formats

All API requests and responses use JSON format. Ensure that the `Content-Type` header is set to `application/json` for all requests.

## Authentication

Some endpoints may require authentication. To authenticate, include an `Authorization` header with your API key in the request.

**Example**:

```http
Authorization: Bearer YOUR_API_KEY
```

## Error Handling

The API uses standard HTTP status codes to indicate the success or failure of a request. In case of an error, the response will include an error message and an error code.

**Example Error Response**:

```json
{
  "error": {
    "code": "number",
    "message": "string"
  }
}
```

## Examples

### Example 1: Get Account Information

**Request**:

```http
POST /wallet/getaccount
Content-Type: application/json

{
  "address": "TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
}
```

**Response**:

```json
{
  "address": "TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
  "balance": 1000000,
  "create_time": 1620000000000,
  "latest_opration_time": 1620000000000,
  "latest_consume_free_time": 1620000000000,
  "account_resource": {
    "energy_usage": 0,
    "energy_limit": 0,
    "net_usage": 0,
    "net_limit": 0
  }
}
```

### Example 2: Send Transaction

**Request**:

```http
POST /wallet/createtransaction
Content-Type: application/json

{
  "owner_address": "TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
  "to_address": "TYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY",
  "amount": 1000
}
```

**Response**:

```json
{
  "txID": "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
  "raw_data": {
    "contract": [
      {
        "parameter": {
          "value": {
            "amount": 1000,
            "owner_address": "TXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX",
            "to_address": "TYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY"
          },
          "type_url": "type.googleapis.com/protocol.TransferContract"
        },
        "type": "TransferContract"
      }
    ],
    "ref_block_bytes": "0000",
    "ref_block_hash": "0000000000000000",
    "expiration": 1620000000000,
    "timestamp": 1620000000000
  },
  "raw_data_hex": "0a0200002208000000000000000040e1f50552e1f505"
}
```

## Additional Resources

For more information and resources, refer to the following:

- [TRON Protocol Documentation](https://developers.tron.network/docs)
- [TRON GitHub Repository](https://github.com/tronprotocol)
- [TRON Community Forum](https://forum.tron.network)
