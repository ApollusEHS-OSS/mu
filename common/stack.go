package common

import (
	"bytes"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/cloudformation"
	"github.com/aws/aws-sdk-go/service/cloudformation/cloudformationiface"
	"github.com/op/go-logging"
	"io"
)

var log = logging.MustGetLogger("stack")

// StackWaiter for waiting on stack status to be final
type StackWaiter interface {
	AwaitFinalStatus(stackName string) string
}

// StackUpserter for applying changes to a stack
type StackUpserter interface {
	UpsertStack(stackName string, templateBodyReader io.Reader, stackParameters map[string]string) error
}

// StackManager composite of all stack capabilities
type StackManager interface {
	StackUpserter
	StackWaiter
}

type cloudformationStackManager struct {
	cfnAPI cloudformationiface.CloudFormationAPI
}

// TODO: support "dry-run" and write the template to a file
// fmt.Sprintf("%s/%s.yml", os.TempDir(), name),

// NewStackManager creates a new StackManager backed by cloudformation
func newStackManager(region string) (StackManager, error) {
	sess, err := session.NewSession()
	if err != nil {
		return nil, err
	}
	log.Debugf("Connecting to CloudFormation service in region:%s", region)
	cfn := cloudformation.New(sess, &aws.Config{Region: aws.String(region)})
	return &cloudformationStackManager{
		cfnAPI: cfn,
	}, nil
}

func buildStackParameters(stackParameters map[string]string) []*cloudformation.Parameter {
	parameters := make([]*cloudformation.Parameter, 0, len(stackParameters))
	for key, value := range stackParameters {
		parameters = append(parameters,
			&cloudformation.Parameter{
				ParameterKey:   aws.String(key),
				ParameterValue: aws.String(value),
			})
	}
	return parameters
}

// UpsertStack will create/update the cloudformation stack
func (cfnMgr *cloudformationStackManager) UpsertStack(stackName string, templateBodyReader io.Reader, stackParameters map[string]string) error {
	stackStatus := cfnMgr.AwaitFinalStatus(stackName)

	// load the template
	templateBodyBytes := new(bytes.Buffer)
	templateBodyBytes.ReadFrom(templateBodyReader)
	templateBody := aws.String(templateBodyBytes.String())

	parameters := buildStackParameters(stackParameters)

	cfnAPI := cfnMgr.cfnAPI
	if stackStatus == "" {

		log.Debugf("  Creating stack named '%s'", stackName)
		log.Debugf("  Stack parameters:\n\t%s", parameters)
		params := &cloudformation.CreateStackInput{
			StackName: aws.String(stackName),
			Capabilities: []*string{
				aws.String(cloudformation.CapabilityCapabilityIam),
			},
			Parameters:   parameters,
			TemplateBody: templateBody,
		}
		_, err := cfnAPI.CreateStack(params)
		log.Debug("  Create stack complete err=%s", err)
		if err != nil {
			return err
		}

		waitParams := &cloudformation.DescribeStacksInput{
			StackName: aws.String(stackName),
		}
		log.Debug("  Waiting for stack to exist...")
		cfnAPI.WaitUntilStackExists(waitParams)
		log.Debug("  Stack exists.")

	} else {
		log.Debugf("  Updating stack named '%s'", stackName)
		log.Debugf("  Prior state: %s", stackStatus)
		log.Debugf("  Stack parameters:\n\t%s", parameters)
		params := &cloudformation.UpdateStackInput{
			StackName: aws.String(stackName),
			Capabilities: []*string{
				aws.String(cloudformation.CapabilityCapabilityIam),
			},
			Parameters:   parameters,
			TemplateBody: templateBody,
		}

		_, err := cfnAPI.UpdateStack(params)
		log.Debug("  Update stack complete err=%s", err)
		if err != nil {
			if awsErr, ok := err.(awserr.Error); ok {
				if awsErr.Code() == "ValidationError" && awsErr.Message() == "No updates are to be performed." {
					log.Noticef("  No changes for stack '%s'", stackName)
					return nil
				}
			}
			return err
		}

	}
	return nil
}

// AwaitFinalStatus waits for the stack to arrive in a final status
//  returns: final status, or empty string if stack doesn't exist
func (cfnMgr *cloudformationStackManager) AwaitFinalStatus(stackName string) string {
	cfnAPI := cfnMgr.cfnAPI
	params := &cloudformation.DescribeStacksInput{
		StackName: aws.String(stackName),
	}
	resp, err := cfnAPI.DescribeStacks(params)

	if err == nil && resp != nil && len(resp.Stacks) == 1 {
		switch *resp.Stacks[0].StackStatus {
		case cloudformation.StackStatusReviewInProgress,
			cloudformation.StackStatusCreateInProgress,
			cloudformation.StackStatusRollbackInProgress:
			// wait for create
			log.Debugf("  Waiting for stack:%s to complete...current status=%s", stackName, *resp.Stacks[0].StackStatus)
			cfnAPI.WaitUntilStackCreateComplete(params)
			resp, err = cfnAPI.DescribeStacks(params)
		case cloudformation.StackStatusDeleteInProgress:
			// wait for delete
			log.Debugf("  Waiting for stack:%s to delete...current status=%s", stackName, *resp.Stacks[0].StackStatus)
			cfnAPI.WaitUntilStackDeleteComplete(params)
			resp, err = cfnAPI.DescribeStacks(params)
		case cloudformation.StackStatusUpdateInProgress,
			cloudformation.StackStatusUpdateRollbackInProgress,
			cloudformation.StackStatusUpdateCompleteCleanupInProgress,
			cloudformation.StackStatusUpdateRollbackCompleteCleanupInProgress:
			// wait for update
			log.Debugf("  Waiting for stack:%s to update...current status=%s", stackName, *resp.Stacks[0].StackStatus)
			cfnAPI.WaitUntilStackUpdateComplete(params)
			resp, err = cfnAPI.DescribeStacks(params)
		case cloudformation.StackStatusCreateFailed,
			cloudformation.StackStatusCreateComplete,
			cloudformation.StackStatusRollbackFailed,
			cloudformation.StackStatusRollbackComplete,
			cloudformation.StackStatusDeleteFailed,
			cloudformation.StackStatusDeleteComplete,
			cloudformation.StackStatusUpdateComplete,
			cloudformation.StackStatusUpdateRollbackFailed,
			cloudformation.StackStatusUpdateRollbackComplete:
			// no op

		}
		log.Debugf("  Returning final status for stack:%s ... status=%s", stackName, *resp.Stacks[0].StackStatus)
		return *resp.Stacks[0].StackStatus
	}

	log.Debugf("  Stack doesn't exist ... stack=%s", stackName)
	return ""
}
