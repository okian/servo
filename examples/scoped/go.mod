module example.com/servoscoped

go 1.27.0

require github.com/okian/servo/v3 v3.0.0

require go.uber.org/goleak v1.3.0 // indirect

replace github.com/okian/servo/v3 => ../..
